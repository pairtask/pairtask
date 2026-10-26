package peer

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestAcceptanceClientRequiresExpectedServerIdentity(t *testing.T) {
	p := acceptanceStart(t, GateConfig{})
	cert := acceptanceCertificate(t, "alice")
	wrong := acceptanceLeaf(t, acceptanceCertificate(t, "other-gate"))
	for _, server := range []*x509.Certificate{nil, wrong} {
		client, err := Dial(context.Background(), p.addr, ClientConfig{Cert: cert, Server: server, Timeout: time.Second})
		if err == nil {
			client.Close()
			t.Fatal("untrusted server accepted")
		}
	}
	client, err := Dial(context.Background(), p.addr, ClientConfig{Cert: cert, Server: acceptanceLeaf(t, p.serverCert), Timeout: time.Second})
	acceptanceNoError(t, err)
	defer client.Close()
	if client.ServerFingerprint() != acceptanceFingerprint(t, p.serverCert) {
		t.Fatal("client recorded wrong server identity")
	}
	reply, err := client.Send(context.Background(), acceptanceRecord("pinned"))
	acceptanceNoError(t, err)
	acceptanceStatus(t, reply, StatusPending)
}

func TestAcceptanceMissingClientCertificateCannotCreateRequest(t *testing.T) {
	p := acceptanceStart(t, GateConfig{})
	client, err := Dial(context.Background(), p.addr, ClientConfig{Server: acceptanceLeaf(t, p.serverCert), Timeout: time.Second})
	if err == nil {
		defer client.Close()
		if reply, err := client.Send(context.Background(), acceptanceRecord("anonymous")); err == nil && reply.Status != StatusRefused {
			t.Fatalf("anonymous request accepted: %+v", reply)
		}
	}
	total, _, err := p.store.Pending(context.Background(), "")
	if err != nil || total != 0 {
		t.Fatalf("anonymous request stored: count=%d, error=%v", total, err)
	}
}

func TestAcceptanceBlockedCertificateCannotCreateRequest(t *testing.T) {
	cert := acceptanceCertificate(t, "blocked")
	fp := acceptanceFingerprint(t, cert)
	p := acceptanceStart(t, GateConfig{Blocked: PinSet{fp: true}})
	c := p.connect(t, cert)
	json.NewEncoder(c).Encode(acceptanceRecord("blocked"))
	var reply Reply
	if err := json.NewDecoder(c).Decode(&reply); err == nil && reply.Status != StatusRefused {
		t.Fatalf("blocked peer admitted: %+v", reply)
	}
	if _, ok, err := p.store.Get(context.Background(), ContactRef{Fingerprint: fp, RequestID: "blocked"}); err != nil || ok {
		t.Fatalf("blocked request stored: %v,%v", ok, err)
	}
}

func TestAcceptanceGateShutdownClosesEstablishedSessions(t *testing.T) {
	p := acceptanceStart(t, GateConfig{Limits: Limits{IdleTimeout: time.Minute}})
	c := p.connect(t, acceptanceCertificate(t, "alice"))
	acceptanceStatus(t, acceptanceSend(t, c, acceptanceRecord("before-stop")), StatusPending)
	p.stop()
	c.SetDeadline(time.Now().Add(300 * time.Millisecond))
	if err := json.NewEncoder(c).Encode(acceptanceRecord("after-stop")); err == nil {
		var reply Reply
		if err := json.NewDecoder(c).Decode(&reply); err == nil {
			t.Fatalf("stopped gate served a request: %+v", reply)
		}
	}
	total, _, err := p.store.Pending(context.Background(), "")
	if err != nil || total != 1 {
		t.Fatalf("state changed after shutdown: %d, %v", total, err)
	}
}

func TestAcceptanceRelayCannotModifyAuthenticatedRequest(t *testing.T) {
	p := acceptanceStart(t, GateConfig{})
	cert := acceptanceCertificate(t, "alice")
	c := p.connect(t, cert)
	p.wire.tamperNext.Store(true)
	json.NewEncoder(c).Encode(acceptanceRecord("tampered"))
	var reply Reply
	if err := json.NewDecoder(c).Decode(&reply); err == nil {
		t.Fatalf("tampered TLS record produced reply: %+v", reply)
	}
	if p.wire.tamperNext.Load() {
		t.Fatal("tampering was not exercised")
	}
	if _, ok, err := p.store.Get(context.Background(), ContactRef{Fingerprint: acceptanceFingerprint(t, cert), RequestID: "tampered"}); err != nil || ok {
		t.Fatalf("tampered request stored: %v,%v", ok, err)
	}
}

func TestAcceptanceIncompleteHandshakeReleasesCapacity(t *testing.T) {
	p := acceptanceStart(t, GateConfig{Limits: Limits{MaxConns: 1, IdleTimeout: 100 * time.Millisecond}})
	raw, err := net.DialTimeout("tcp", p.addr, time.Second)
	acceptanceNoError(t, err)
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(time.Second))
	raw.Write([]byte{0x16}) // Incomplete TLS handshake header.
	var b [1]byte
	if _, err := raw.Read(b[:]); err == nil {
		t.Fatal("incomplete TLS connection remained usable")
	} else if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("gate did not enforce handshake timeout")
	}
	acceptanceRetryAfterIdle(t, p)
}

func acceptanceRetryAfterIdle(t *testing.T, p *acceptanceProbe) {
	t.Helper()
	cert := acceptanceCertificate(t, "alice")
	deadline := time.Now().Add(time.Second)
	for {
		client, err := Dial(context.Background(), p.addr, ClientConfig{Cert: cert, Server: acceptanceLeaf(t, p.serverCert), Timeout: time.Second})
		if err == nil {
			reply, sendErr := client.Send(context.Background(), acceptanceRecord("after-idle"))
			client.Close()
			if sendErr == nil {
				acceptanceStatus(t, reply, StatusPending)
				break
			}
			err = sendErr
		}
		if time.Now().After(deadline) {
			t.Fatalf("connection slot not recovered: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func acceptanceEchoRelay(t *testing.T, config RelayConfig) net.Conn {
	t.Helper()
	config.Dial = func(ctx context.Context) (net.Conn, error) {
		a, b := net.Pipe()
		go func() { defer b.Close(); io.Copy(b, b) }()
		return a, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	acceptanceNoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- NewRelay(config).Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		ln.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("relay did not stop")
		}
	})
	c, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	acceptanceNoError(t, err)
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(2 * time.Second))
	return c
}

func TestAcceptanceRelayByteBound(t *testing.T) {
	c := acceptanceEchoRelay(t, RelayConfig{MaxBytes: 16, IdleTimeout: time.Second})
	if _, err := c.Write([]byte("12345678")); err != nil {
		t.Fatal(err)
	}
	var first [8]byte
	if _, err := io.ReadFull(c, first[:]); err != nil {
		t.Fatal(err)
	}
	if string(first[:]) != "12345678" {
		t.Fatal("relay did not preserve bytes below the bound")
	}
	if _, err := c.Write([]byte(strings.Repeat("x", 9))); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(c)
	// Closing with unread bytes can reset TCP; either reset or EOF is valid.
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("relay did not close at its byte bound")
	}
	if len(data)+len(first) > 16 {
		t.Fatalf("relay exceeded byte bound: %d", len(data)+len(first))
	}
}

func TestAcceptanceRelayIdleTimeoutTracksActivity(t *testing.T) {
	c := acceptanceEchoRelay(t, RelayConfig{IdleTimeout: 180 * time.Millisecond})
	started := time.Now()
	ticker := time.NewTicker(60 * time.Millisecond)
	defer ticker.Stop()
	for i := 0; i < 6; i++ {
		<-ticker.C
		if _, err := c.Write([]byte("x")); err != nil {
			t.Fatalf("active connection closed after %v: %v", time.Since(started), err)
		}
		var b [1]byte
		if _, err := io.ReadFull(c, b[:]); err != nil {
			t.Fatalf("active connection closed after %v: %v", time.Since(started), err)
		}
	}
	var b [1]byte
	if _, err := c.Read(b[:]); err == nil {
		t.Fatal("idle connection was not closed")
	}
}

// A peer controlling its own TLS endpoint still cannot invent a reply that the
// client mistakes for a correlated response to a different request.
func TestAcceptanceClientValidatesReplyEnvelope(t *testing.T) {
	for _, field := range []string{"version", "request-id", "status"} {
		t.Run(field, func(t *testing.T) {
			addr, cert, done := acceptanceBadReplyServer(t, field)
			client, err := Dial(context.Background(), addr, ClientConfig{Cert: acceptanceCertificate(t, "alice"), Server: acceptanceLeaf(t, cert), Timeout: time.Second})
			acceptanceNoError(t, err)
			defer client.Close()
			if reply, err := client.Send(context.Background(), acceptanceRecord("actual-request")); err == nil {
				t.Fatalf("invalid %s accepted: %+v", field, reply)
			}
			<-done
		})
	}
}

func acceptanceBadReplyServer(t *testing.T, field string) (string, tls.Certificate, <-chan struct{}) {
	t.Helper()
	cert := acceptanceCertificate(t, "fake-gate")
	ln := acceptanceListen(t, "tcp", "127.0.0.1:0")
	done := make(chan struct{})
	go func() {
		defer close(done)
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		conn := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
		conn.SetDeadline(time.Now().Add(time.Second))
		var rec Record
		if json.NewDecoder(conn).Decode(&rec) != nil {
			return
		}
		reply := Reply{Version: SchemaVersion, RequestID: rec.RequestID, Status: StatusPending}
		switch field {
		case "version":
			reply.Version++
		case "request-id":
			reply.RequestID = "different"
		case "status":
			reply.Status = "made-up"
		}
		json.NewEncoder(conn).Encode(reply)
	}()
	return ln.Addr().String(), cert, done
}
