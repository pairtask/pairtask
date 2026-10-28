//go:build unix

package firstcontact_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/peer/messaging"
)

type messagePolicy struct {
	pin     string
	options firstcontact.ServerOptions
}

func messageHandshake(t *testing.T, alice, bob *peer.WorkspaceIdentity, policy messagePolicy) (connResult, connResult) {
	t.Helper()
	ctx := testContext(t)
	a, b := tcpPipe(t)
	done := make(chan connResult, 1)
	go func() {
		c, e := firstcontact.ServerWith(ctx, a, alice, time.Now, policy.options)
		done <- connResult{c, e}
	}()
	client, err := firstcontact.MessageClient(ctx, b, bob, time.Now, policy.pin)
	server := <-done
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

func TestMessageTLSAcceptanceSeparateALPNAndMutualPins(t *testing.T) {
	alice, bob := identity(t), identity(t)
	opts := firstcontact.ServerOptions{ContactPins: func(string) bool { return false }, MessagePins: func(fp string) bool { return fp == bob.Fingerprint() }}
	server, client := messageHandshake(t, alice, bob, messagePolicy{alice.Fingerprint(), opts})
	mustOK(t, server.err)
	mustOK(t, client.err)
	equal(t, firstcontact.MessageALPN, messaging.ALPN)
	equal(t, server.conn.Protocol(), messaging.ALPN)
	equal(t, client.conn.Protocol(), messaging.ALPN)
	equal(t, server.conn.Connection().PeerLeaf, bob.Fingerprint())
	equal(t, client.conn.Connection().PeerLeaf, alice.Fingerprint())
	ctx := testContext(t)
	payload := bytes.Repeat([]byte{'x'}, messaging.MaxFrameBytes)
	mustOK(t, client.conn.MessageFrames(ctx).Send(payload))
	got, err := server.conn.MessageFrames(ctx).Recv()
	mustOK(t, err)
	equal(t, bytes.Equal(got, payload), true)
	mustFail(t, client.conn.MessageFrames(ctx).Send(append(payload, 'x')))
}

func TestMessageTLSAcceptanceSettlementPinCannotAuthorizeMessages(t *testing.T) {
	alice, bob := identity(t), identity(t)
	opts := firstcontact.ServerOptions{ContactPins: func(string) bool { return true }, MessagePins: func(string) bool { return false }}
	server, _ := messageHandshake(t, alice, bob, messagePolicy{alice.Fingerprint(), opts})
	mustFail(t, server.err)
	if server.conn != nil {
		t.Fatal("settlement-only contact reached message protocol")
	}
}

func TestMessageTLSAcceptanceOldPeerHasNoProtocolFallback(t *testing.T) {
	alice, bob := identity(t), identity(t)
	opts := firstcontact.ServerOptions{ContactPins: func(string) bool { return true }}
	server, client := messageHandshake(t, alice, bob, messagePolicy{alice.Fingerprint(), opts})
	mustFail(t, server.err)
	mustFail(t, client.err)
	if server.conn != nil || client.conn != nil {
		t.Fatal("message connection fell back to settlement/pairing")
	}
	if !firstcontact.NoCommonProtocol(client.err) {
		t.Fatalf("old-peer ALPN alert not recognized: %v", client.err)
	}
}

func TestMessageTLSAcceptancePinFailureIsNotUnsupported(t *testing.T) {
	alice, bob, replacement := identity(t), identity(t), identity(t)
	opts := firstcontact.ServerOptions{MessagePins: func(string) bool { return true }}
	_, client := messageHandshake(t, replacement, bob, messagePolicy{alice.Fingerprint(), opts})
	if !errors.Is(client.err, peer.ErrPeerMismatch) {
		t.Fatalf("wrong pin error %v", client.err)
	}
	equal(t, firstcontact.NoCommonProtocol(client.err), false)
	for _, err := range []error{nil, context.DeadlineExceeded, peer.ErrPeerMismatch, errors.New("tls: no application protocol"), errors.New("dial failed: tls: no application protocol")} {
		if firstcontact.NoCommonProtocol(err) {
			t.Fatalf("non-alert classified unsupported: %v", err)
		}
	}
}

func TestMessageTLSAcceptanceFramesStopOnContextCancellation(t *testing.T) {
	alice, bob := identity(t), identity(t)
	opts := firstcontact.ServerOptions{MessagePins: func(string) bool { return true }}
	server, client := messageHandshake(t, alice, bob, messagePolicy{alice.Fingerprint(), opts})
	mustOK(t, server.err)
	mustOK(t, client.err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := server.conn.MessageFrames(ctx).Recv(); done <- err }()
	cancel()
	select {
	case err := <-done:
		mustFail(t, err)
	case <-time.After(time.Second):
		t.Fatal("cancelled message receive stayed blocked")
	}
}
