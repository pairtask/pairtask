//go:build unix

package firstcontact_test

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
)

// Each mutation changes one property of an otherwise valid TLS peer.
func invalidTLSCases(t *testing.T, self, other *peer.WorkspaceIdentity) map[string]func(*tls.Config) {
	t.Helper()
	return map[string]func(*tls.Config){
		"missing-certificate": func(c *tls.Config) { c.Certificates = nil },
		"two-certificates": func(c *tls.Config) {
			c.Certificates[0].Certificate = append(c.Certificates[0].Certificate, c.Certificates[0].Certificate[0])
		},
		"wrong-alpn": func(c *tls.Config) { c.NextProtos = []string{"operator/1"} },
		"no-alpn":    func(c *tls.Config) { c.NextProtos = nil },
		"tls12":      func(c *tls.Config) { c.MinVersion = tls.VersionTLS12; c.MaxVersion = tls.VersionTLS12 },
		"self":       func(c *tls.Config) { c.Certificates = []tls.Certificate{certificate(t, self, time.Now())} },
		"expired": func(c *tls.Config) {
			c.Certificates = []tls.Certificate{certificate(t, other, time.Now().Add(-2*time.Hour))}
		},
		"future": func(c *tls.Config) {
			c.Certificates = []tls.Certificate{certificate(t, other, time.Now().Add(2*time.Hour))}
		},
		"non-ed25519": func(c *tls.Config) {
			cert, err := peer.SelfSigned("ecdsa")
			mustOK(t, err)
			c.Certificates = []tls.Certificate{cert}
		},
	}
}

func TestFirstContactAcceptanceServerRejectsInvalidTLS(t *testing.T) {
	alice, bob := identity(t), identity(t)
	for kind, mutate := range invalidTLSCases(t, alice, bob) {
		t.Run(kind, func(t *testing.T) {
			cfg := rawClientConfig(certificate(t, bob, time.Now()))
			mutate(cfg)
			r, _ := rawClient(t, alice, cfg)
			mustFail(t, r.err)
			equal(t, r.conn == nil, true)
			equal(t, errors.Is(r.err, firstcontact.ErrFirstContact), true)
			if kind == "self" {
				equal(t, errors.Is(r.err, peer.ErrSelfPairing), true)
			}
		})
	}
}

func TestFirstContactAcceptanceClientRejectsInvalidTLS(t *testing.T) {
	alice, bob := identity(t), identity(t)
	for kind, mutate := range invalidTLSCases(t, bob, alice) {
		t.Run(kind, func(t *testing.T) {
			cert := certificate(t, alice, time.Now())
			cfg := &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, NextProtos: []string{firstcontact.ALPN}, Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAnyClientCert}
			mutate(cfg)
			ctx := testContext(t)
			a, b := tcpPipe(t)
			done := make(chan error, 1)
			go func() { done <- tls.Server(a, cfg).HandshakeContext(ctx) }()
			c, err := firstcontact.Client(ctx, b, bob, time.Now)
			mustFail(t, err)
			equal(t, c == nil, true)
			equal(t, errors.Is(err, firstcontact.ErrFirstContact), true)
			if kind == "self" {
				equal(t, errors.Is(err, peer.ErrSelfPairing), true)
			}
			b.Close()
			a.Close()
			<-done
		})
	}
}

func TestFirstContactAcceptanceHandshakeCancellationClosesRaw(t *testing.T) {
	for _, server := range []bool{false, true} {
		t.Run(map[bool]string{false: "client", true: "server"}[server], func(t *testing.T) {
			ctx, cancel := context.WithCancel(testContext(t))
			a, b := pipe(t)
			id := identity(t)
			mustOK(t, b.SetReadDeadline(time.Now().Add(time.Second)))
			done := make(chan error, 1)
			go func() {
				var err error
				if server {
					_, err = firstcontact.Server(ctx, a, id, time.Now)
				} else {
					_, err = firstcontact.Client(ctx, a, id, time.Now)
				}
				done <- err
			}()
			select {
			case e := <-done:
				t.Fatalf("handshake returned before cancellation: %v", e)
			case <-time.After(20 * time.Millisecond):
			}
			cancel()
			select {
			case err := <-done:
				equal(t, errors.Is(err, context.Canceled), true)
			case <-time.After(time.Second):
				t.Fatal("handshake ignored cancellation")
			}
			_, err := b.Read(make([]byte, 1))
			mustFail(t, err)
			if e, ok := err.(interface{ Timeout() bool }); ok && e.Timeout() {
				t.Fatal("failed handshake left raw connection open")
			}
		})
	}
}

func TestFirstContactAcceptanceExporterMatchesRFC9266(t *testing.T) {
	r, client := rawClient(t, identity(t), rawClientConfig(certificate(t, identity(t), time.Now())))
	mustOK(t, r.err)
	state := client.ConnectionState()
	equal(t, state.Version, uint16(tls.VersionTLS13))
	equal(t, state.NegotiatedProtocol, firstcontact.ALPN)
	equal(t, state.DidResume, false)
	exported, err := state.ExportKeyingMaterial("EXPORTER-Channel-Binding", nil, 32)
	mustOK(t, err)
	equal(t, string(exported), string(r.conn.Connection().ChannelBinding))
}
