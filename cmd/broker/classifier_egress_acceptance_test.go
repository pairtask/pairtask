package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/classifier"
	"github.com/pairtask/pairtask/internal/peer/egress"
)

func TestClassifierProviderUsesConnectorEgressWithTLS(t *testing.T) {
	for _, tc := range []struct {
		name, provider, account, model, destination string
	}{
		{"jev", classifier.ProviderTypeSafe, "", "jev-latest", "api.typesafe.ai:443"},
		{"clef", classifier.ProviderWorkersAI, "account-id", "clef", "api.cloudflare.com:443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, observed := captureClassifierEgress(t)
			transport, route := classifierTransport(config{PairingEgressSocket: path})
			if transport == nil || route == "direct" {
				t.Fatalf("configured egress was not selected: route=%q", route)
			}
			const key = "secret-must-stay-in-tls"
			client, err := classifier.NewClient(classifier.Credential{
				Provider: tc.provider, Account: tc.account, Key: key,
			}, 2*time.Second, transport)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Ask(context.Background(), classifier.Request{
				Model: tc.model, State: "choose one", Instructions: "select an agent",
				Criteria: map[string]string{"a1": "tester", "none": "abstain"},
			})
			if err == nil {
				t.Fatal("fake forwarder provided no provider reply")
			}
			assertClassifierEgress(t, observed, tc.destination, key)
		})
	}
}

type classifierEgressObservation struct {
	destination string
	first       []byte
	err         error
}

func captureClassifierEgress(t *testing.T) (string, <-chan classifierEgressObservation) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "egress.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	observed := make(chan classifierEgressObservation, 1)
	go func() { observed <- readClassifierEgress(listener) }()
	return path, observed
}

func readClassifierEgress(listener net.Listener) classifierEgressObservation {
	connection, err := listener.Accept()
	if err != nil {
		return classifierEgressObservation{err: err}
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(3 * time.Second))
	var header [3]byte
	if _, err := io.ReadFull(connection, header[:]); err != nil {
		return classifierEgressObservation{err: err}
	}
	if header[0] != egress.Version {
		return classifierEgressObservation{err: io.ErrUnexpectedEOF}
	}
	destination := make([]byte, int(binary.BigEndian.Uint16(header[1:])))
	if _, err := io.ReadFull(connection, destination); err != nil {
		return classifierEgressObservation{err: err}
	}
	if _, err := connection.Write([]byte{egress.StatusConnected}); err != nil {
		return classifierEgressObservation{err: err}
	}
	var first [1024]byte
	n, err := connection.Read(first[:])
	return classifierEgressObservation{destination: string(destination), first: append([]byte(nil), first[:n]...), err: err}
}

func assertClassifierEgress(t *testing.T, observed <-chan classifierEgressObservation, destination, key string) {
	t.Helper()
	select {
	case got := <-observed:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.destination != destination {
			t.Fatalf("forwarder destination = %q, want %q", got.destination, destination)
		}
		if len(got.first) == 0 || got.first[0] != 0x16 {
			t.Fatalf("first tunneled bytes are not a TLS handshake: %x", got.first)
		}
		assertNoClassifierCredential(t, got.first, key)
	case <-time.After(3 * time.Second):
		t.Fatal("provider request never reached the forwarder")
	}
}

func assertNoClassifierCredential(t *testing.T, first []byte, key string) {
	t.Helper()
	if bytes.Contains(first, []byte(key)) || bytes.Contains(first, []byte("Authorization")) {
		t.Fatal("credential crossed the forwarder in clear text")
	}
}

func TestClassifierStandaloneTransportRemainsDirect(t *testing.T) {
	transport, route := classifierTransport(config{})
	if transport != nil || route != "direct" {
		t.Fatalf("standalone transport = %T, route = %q", transport, route)
	}
}
