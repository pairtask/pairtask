//go:build unix

package firstcontact_test

import (
	"errors"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
)

func spaceHandshake(t *testing.T, serverID, clientID *peer.WorkspaceIdentity, pin string, options firstcontact.ServerOptions) (connResult, connResult) {
	t.Helper()
	ctx := testContext(t)
	a, b := tcpPipe(t)
	result := make(chan connResult, 1)
	go func() {
		conn, err := firstcontact.ServerWith(ctx, a, serverID, time.Now, options)
		result <- connResult{conn, err}
	}()
	conn, err := firstcontact.SpaceClient(ctx, b, clientID, time.Now, pin)
	// A rejected client (and the initial stub) must release the server read.
	if err != nil {
		b.Close()
	}
	server := <-result
	t.Cleanup(func() {
		a.Close()
		b.Close()
		if conn != nil {
			conn.Close()
		}
		if server.conn != nil {
			server.conn.Close()
		}
	})
	return server, connResult{conn, err}
}

func TestSpaceTLSAcceptanceMutualIdentityAndSeparateProtocol(t *testing.T) {
	alice, bob := identity(t), identity(t)
	opts := firstcontact.ServerOptions{SpacePins: func(key string) bool { return key == bob.Fingerprint() }}
	server, client := spaceHandshake(t, alice, bob, alice.Fingerprint(), opts)
	mustOK(t, server.err)
	mustOK(t, client.err)
	equal(t, firstcontact.SpaceALPN, "pairtask-space/1")
	equal(t, server.conn.Protocol(), firstcontact.SpaceALPN)
	equal(t, client.conn.Protocol(), firstcontact.SpaceALPN)
	equal(t, server.conn.Connection().PeerLeaf, bob.Fingerprint())
	equal(t, client.conn.Connection().PeerLeaf, alice.Fingerprint())
	ctx := testContext(t)
	mustOK(t, client.conn.Frames(ctx).Send([]byte{1, 2}))
	got, err := server.conn.Frames(ctx).Recv()
	mustOK(t, err)
	equal(t, string(got), "\x01\x02")
}

func TestSpaceTLSAcceptanceChangedServerCannotInheritPin(t *testing.T) {
	alice, bob, replacement := identity(t), identity(t), identity(t)
	_, client := spaceHandshake(t, replacement, bob, alice.Fingerprint(), firstcontact.ServerOptions{SpacePins: func(string) bool { return true }})
	equal(t, errors.Is(client.err, peer.ErrPeerMismatch), true)
	equal(t, client.conn == nil, true)
}

func TestSpaceTLSAcceptanceRejectsUnknownClient(t *testing.T) {
	alice, bob := identity(t), identity(t)
	server, _ := spaceHandshake(t, alice, bob, alice.Fingerprint(), firstcontact.ServerOptions{SpacePins: func(string) bool { return false }})
	mustFail(t, server.err)
	equal(t, server.conn == nil, true)
}

func TestSpaceTLSAcceptanceNeverFallsBackToContactOrPairing(t *testing.T) {
	alice, bob := identity(t), identity(t)
	for name, options := range map[string]firstcontact.ServerOptions{
		"pairing-only":                 {},
		"contact-only":                 {ContactPins: func(string) bool { return true }},
		"space-denied-contact-allowed": {SpacePins: func(string) bool { return false }, ContactPins: func(string) bool { return true }},
	} {
		t.Run(name, func(t *testing.T) {
			server, client := spaceHandshake(t, alice, bob, alice.Fingerprint(), options)
			mustFail(t, server.err)
			equal(t, server.conn == nil, true)
			// TLS 1.3 client success can precede the server's rejection; if a
			// connection is returned it must never negotiate another protocol.
			if client.conn != nil {
				equal(t, client.conn.Protocol(), firstcontact.SpaceALPN)
			}
		})
	}
}

func TestSpaceTLSAcceptanceRequiresWellFormedTargetPin(t *testing.T) {
	alice, bob := identity(t), identity(t)
	for _, pin := range []string{"", "sha256:wrong"} {
		t.Run(pin, func(t *testing.T) {
			_, client := spaceHandshake(t, alice, bob, pin, firstcontact.ServerOptions{SpacePins: func(string) bool { return true }})
			mustFail(t, client.err)
			equal(t, client.conn == nil, true)
		})
	}
}
