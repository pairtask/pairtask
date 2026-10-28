//go:build unix

package firstcontact_test

import (
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
)

type contactPolicy struct {
	serverPin     string
	clientAllowed func(string) bool
}

func contactHandshake(t *testing.T, alice, bob *peer.WorkspaceIdentity, policy contactPolicy) (connResult, connResult) {
	t.Helper()
	ctx := testContext(t)
	a, b := tcpPipe(t)
	result := make(chan connResult, 1)
	go func() {
		conn, err := firstcontact.ServerWith(ctx, a, alice, time.Now, firstcontact.ServerOptions{ContactPins: policy.clientAllowed})
		result <- connResult{conn, err}
	}()
	client, err := firstcontact.ContactClient(ctx, b, bob, time.Now, policy.serverPin)
	server := <-result
	t.Cleanup(func() {
		a.Close()
		b.Close()
		if client != nil {
			client.Close()
		}
		if server.conn != nil {
			server.conn.Close()
		}
	})
	return server, connResult{client, err}
}

func TestContactAcceptanceMutualPinsAndSeparateProtocol(t *testing.T) {
	alice, bob := identity(t), identity(t)
	server, client := contactHandshake(t, alice, bob, contactPolicy{alice.Fingerprint(), func(fp string) bool { return fp == bob.Fingerprint() }})
	mustOK(t, server.err)
	mustOK(t, client.err)
	equal(t, server.conn.Protocol(), firstcontact.ContactALPN)
	equal(t, client.conn.Protocol(), firstcontact.ContactALPN)
	equal(t, client.conn.Connection().PeerLeaf, alice.Fingerprint())
	equal(t, server.conn.Connection().PeerLeaf, bob.Fingerprint())
	ctx := testContext(t)
	mustOK(t, client.conn.Frames(ctx).Send([]byte("authenticated settlement")))
	got, err := server.conn.Frames(ctx).Recv()
	mustOK(t, err)
	equal(t, string(got), "authenticated settlement")
}

func TestContactAcceptanceChangedServerKeyCannotInheritPin(t *testing.T) {
	alice, bob, replacement := identity(t), identity(t), identity(t)
	_, client := contactHandshake(t, replacement, bob, contactPolicy{alice.Fingerprint(), func(string) bool { return true }})
	if !errors.Is(client.err, peer.ErrPeerMismatch) {
		t.Fatalf("changed server key: got %v, want peer mismatch", client.err)
	}
	if client.conn != nil {
		t.Fatal("changed server key returned an authenticated connection")
	}
}

func TestContactAcceptanceUnknownClientRejectedByServer(t *testing.T) {
	alice, bob, stranger := identity(t), identity(t), identity(t)
	server, _ := contactHandshake(t, alice, stranger, contactPolicy{alice.Fingerprint(), func(fp string) bool { return fp == bob.Fingerprint() }})
	mustFail(t, server.err)
	if server.conn != nil {
		t.Fatal("unknown client reached contact protocol")
	}
}

func TestContactAcceptanceNeverFallsBackToPairing(t *testing.T) {
	alice, bob := identity(t), identity(t)
	server, client := contactHandshake(t, alice, bob, contactPolicy{alice.Fingerprint(), nil})
	mustFail(t, server.err)
	mustFail(t, client.err)
	for _, pin := range []string{"", "sha256:wrong"} {
		t.Run(pin, func(t *testing.T) {
			_, client := contactHandshake(t, alice, bob, contactPolicy{pin, func(string) bool { return true }})
			mustFail(t, client.err)
		})
	}
}

func TestContactAcceptanceRenewedCertificateKeepsSPKIPin(t *testing.T) {
	alice, bob := identity(t), identity(t)
	ctx := testContext(t)
	a, b := tcpPipe(t)
	result := make(chan connResult, 1)
	go func() {
		conn, err := firstcontact.ServerWith(ctx, a, alice, time.Now, firstcontact.ServerOptions{
			ContactPins: func(fp string) bool { return fp == bob.Fingerprint() },
		})
		result <- connResult{conn, err}
	}()
	cfg := rawClientConfig(certificate(t, bob, time.Now().Add(-time.Minute)))
	cfg.NextProtos = []string{firstcontact.ContactALPN}
	client := tls.Client(b, cfg)
	mustOK(t, client.HandshakeContext(ctx))
	server := <-result
	mustOK(t, server.err)
	equal(t, server.conn.Connection().PeerLeaf, bob.Fingerprint())
	equal(t, server.conn.Protocol(), firstcontact.ContactALPN)
	a.Close()
	b.Close()
	client.Close()
	server.conn.Close()
}

func TestContactAcceptancePairingStillUsesItsOwnALPN(t *testing.T) {
	alice, bob := identity(t), identity(t)
	ctx := testContext(t)
	a, b := tcpPipe(t)
	result := make(chan connResult, 1)
	go func() {
		conn, err := firstcontact.ServerWith(ctx, a, alice, time.Now, firstcontact.ServerOptions{
			ContactPins: func(string) bool { return false },
		})
		result <- connResult{conn, err}
	}()
	client, err := firstcontact.Client(ctx, b, bob, time.Now)
	mustOK(t, err)
	server := <-result
	mustOK(t, server.err)
	equal(t, client.Protocol(), firstcontact.ALPN)
	equal(t, server.conn.Protocol(), firstcontact.ALPN)
	a.Close()
	b.Close()
	client.Close()
	server.conn.Close()
}
