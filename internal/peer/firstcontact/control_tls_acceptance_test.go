//go:build unix

package firstcontact_test

import (
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
)

func controlHandshake(t *testing.T, alice, bob *peer.WorkspaceIdentity, p messagePolicy) (connResult, connResult) {
	t.Helper()
	ctx := testContext(t)
	a, b := tcpPipe(t)
	result := make(chan connResult, 1)
	go func() {
		c, e := firstcontact.ServerWith(ctx, a, alice, time.Now, p.options)
		result <- connResult{c, e}
	}()
	c, e := firstcontact.ControlClient(ctx, b, bob, time.Now, p.pin)
	server := <-result
	return server, connResult{c, e}
}
func TestControlTLSAcceptanceSeparateProtocolWithIndependentPins(t *testing.T) {
	alice, bob := identity(t), identity(t)
	opts := firstcontact.ServerOptions{ContactPins: func(string) bool { return false }, MessagePins: func(string) bool { return false }, ControlPins: func(fp string) bool { return fp == bob.Fingerprint() }}
	server, client := controlHandshake(t, alice, bob, messagePolicy{alice.Fingerprint(), opts})
	mustOK(t, server.err)
	mustOK(t, client.err)
	equal(t, server.conn.Protocol(), "pairtask-control/1")
	equal(t, client.conn.Protocol(), "pairtask-control/1")
	equal(t, server.conn.Connection().PeerLeaf, bob.Fingerprint())
	equal(t, client.conn.Connection().PeerLeaf, alice.Fingerprint())
}
func TestControlTLSAcceptanceNoFallbackToPairingOrMessages(t *testing.T) {
	alice, bob := identity(t), identity(t)
	opts := firstcontact.ServerOptions{ContactPins: func(string) bool { return true }, MessagePins: func(string) bool { return true }}
	server, client := controlHandshake(t, alice, bob, messagePolicy{alice.Fingerprint(), opts})
	mustFail(t, server.err)
	mustFail(t, client.err)
	if server.conn != nil || client.conn != nil {
		t.Fatal("control dial fell back to another protocol")
	}
}
func TestControlTLSAcceptanceBothPinsAreRequired(t *testing.T) {
	alice, bob := identity(t), identity(t)
	opts := firstcontact.ServerOptions{ControlPins: func(string) bool { return false }}
	server, _ := controlHandshake(t, alice, bob, messagePolicy{alice.Fingerprint(), opts})
	mustFail(t, server.err)
	opts.ControlPins = func(string) bool { return true }
	_, client := controlHandshake(t, alice, bob, messagePolicy{bob.Fingerprint(), opts})
	mustFail(t, client.err)
}
