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
			path := filepath.Join(t.TempDir(), "egress.sock")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { listener.Close() })

			type observation struct {
				destination string
				first       []byte
				err         error
			}
			observed := make(chan observation, 1)
			go func() {
				connection, err := listener.Accept()
				if err != nil {
					observed <- observation{err: err}
					return
				}
				defer connection.Close()
				connection.SetDeadline(time.Now().Add(3 * time.Second))
				var header [3]byte
				if _, err := io.ReadFull(connection, header[:]); err != nil {
					observed <- observation{err: err}
					return
				}
				if header[0] != egress.Version {
					observed <- observation{err: io.ErrUnexpectedEOF}
					return
				}
				destination := make([]byte, int(binary.BigEndian.Uint16(header[1:])))
				if _, err := io.ReadFull(connection, destination); err != nil {
					observed <- observation{err: err}
					return
				}
				if _, err := connection.Write([]byte{egress.StatusConnected}); err != nil {
					observed <- observation{err: err}
					return
				}
				var first [1024]byte
				n, err := connection.Read(first[:])
				observed <- observation{destination: string(destination), first: append([]byte(nil), first[:n]...), err: err}
			}()

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
			select {
			case got := <-observed:
				if got.err != nil {
					t.Fatal(got.err)
				}
				if got.destination != tc.destination {
					t.Fatalf("forwarder destination = %q, want %q", got.destination, tc.destination)
				}
				if len(got.first) == 0 || got.first[0] != 0x16 {
					t.Fatalf("first tunneled bytes are not a TLS handshake: %x", got.first)
				}
				if bytes.Contains(got.first, []byte(key)) || bytes.Contains(got.first, []byte("Authorization")) {
					t.Fatal("credential crossed the forwarder in clear text")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("provider request never reached the forwarder")
			}
		})
	}
}

func TestClassifierStandaloneTransportRemainsDirect(t *testing.T) {
	transport, route := classifierTransport(config{})
	if transport != nil || route != "direct" {
		t.Fatalf("standalone transport = %T, route = %q", transport, route)
	}
}
