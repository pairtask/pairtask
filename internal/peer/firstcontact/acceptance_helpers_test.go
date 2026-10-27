//go:build unix

package firstcontact_test

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
)

func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func equal[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}
func mustFail(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("unexpected success")
	}
}

type marker struct{ fingerprint string }

func (m *marker) IdentityFingerprint(context.Context) (string, bool, error) {
	return m.fingerprint, m.fingerprint != "", nil
}
func (m *marker) InitIdentity(_ context.Context, fp string) error { m.fingerprint = fp; return nil }
func identity(t *testing.T) *peer.WorkspaceIdentity {
	t.Helper()
	id, err := peer.LoadOrCreateIdentity(context.Background(), filepath.Join(t.TempDir(), "identity"), &marker{}, rand.Reader)
	mustOK(t, err)
	return id
}
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

type connResult struct {
	conn *firstcontact.Conn
	err  error
}

func pipe(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

// TCP buffers the simultaneous TLS alert/handshake writes a deliberately
// invalid peer can cause; an unbuffered net.Pipe artificially deadlocks them.
func tcpPipe(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	mustOK(t, err)
	defer ln.Close()
	b, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	mustOK(t, err)
	a, err := ln.Accept()
	mustOK(t, err)
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}
func connected(t *testing.T, alice, bob *peer.WorkspaceIdentity) (*firstcontact.Conn, *firstcontact.Conn) {
	t.Helper()
	ctx := testContext(t)
	a, b := pipe(t)
	ch := make(chan connResult, 1)
	go func() { c, e := firstcontact.Server(ctx, a, alice, time.Now); ch <- connResult{c, e} }()
	client, err := firstcontact.Client(ctx, b, bob, time.Now)
	mustOK(t, err)
	r := <-ch
	mustOK(t, r.err)
	t.Cleanup(func() { a.Close(); b.Close(); client.Close(); r.conn.Close() })
	return r.conn, client
}
func certificate(t *testing.T, id *peer.WorkspaceIdentity, at time.Time) tls.Certificate {
	t.Helper()
	c, err := id.Certificate("test-workspace", at, time.Hour)
	mustOK(t, err)
	return c
}
func rawClientConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, NextProtos: []string{firstcontact.ALPN}, Certificates: []tls.Certificate{cert}, InsecureSkipVerify: true}
}

// rawClient connects a configurable TLS peer to the real first-contact server.
func rawClient(t *testing.T, id *peer.WorkspaceIdentity, cfg *tls.Config) (connResult, *tls.Conn) {
	t.Helper()
	ctx := testContext(t)
	a, b := tcpPipe(t)
	ch := make(chan connResult, 1)
	go func() { c, e := firstcontact.Server(ctx, a, id, time.Now); ch <- connResult{c, e} }()
	c := tls.Client(b, cfg)
	_ = c.HandshakeContext(ctx) // A client's TLS success alone does not imply server admission.
	r := <-ch
	t.Cleanup(func() {
		a.Close()
		b.Close()
		c.Close()
		if r.conn != nil {
			r.conn.Close()
		}
	})
	return r, c
}
