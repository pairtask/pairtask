//go:build unix

package firstcontact_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/peer/pake"
)

func TestFirstContactAcceptanceBindsActualKeysAndFreshExporter(t *testing.T) {
	alice, bob := identity(t), identity(t)
	var previous []byte
	for range 2 {
		a, b := connected(t, alice, bob)
		ac, bc := a.Connection(), b.Connection()
		equal(t, ac.PeerLeaf, bob.Fingerprint())
		equal(t, bc.PeerLeaf, alice.Fingerprint())
		equal(t, len(ac.ChannelBinding), 32)
		equal(t, bytes.Equal(ac.ChannelBinding, bc.ChannelBinding), true)
		equal(t, bytes.Equal(previous, ac.ChannelBinding), false)
		previous = bytes.Clone(ac.ChannelBinding)
		ac.ChannelBinding[0] ^= 1
		equal(t, bytes.Equal(a.Connection().ChannelBinding, previous), true)
	}
}

func TestFirstContactAcceptanceTLSAloneCreatesNoPairing(t *testing.T) {
	store := peer.NewMemPairingStore()
	alice, bob := identity(t), identity(t)
	a, b := connected(t, alice, bob)
	ctx := testContext(t)
	invitations, err := peer.NewInvitations(peer.DefaultInvitationPolicy(), time.Now, rand.Reader, store)
	mustOK(t, err)
	_, err = invitations.Open()
	mustOK(t, err)
	inviter, err := peer.NewInviter(pake.New(), invitations, alice)
	mustOK(t, err)
	done := make(chan error, 1)
	go func() { _, err := inviter.Serve(ctx, a.Frames(ctx), a.Connection()); done <- err }()
	hello, err := peer.EncodeJoinerHello(peer.JoinerHello{OperationID: "unauthenticated", Name: "Bob"})
	mustOK(t, err)
	mustOK(t, b.Frames(ctx).Send(hello))
	_, err = b.Frames(ctx).Recv()
	mustOK(t, err)
	// A valid TLS connection and hello still cannot enroll a request.
	// The unauthenticated peer now sends an invalid PAKE message.
	mustOK(t, b.Frames(ctx).Send([]byte("not a PAKE")))
	mustFail(t, <-done)
	requests, err := store.Requests(ctx)
	mustOK(t, err)
	equal(t, len(requests), 0)
}

func TestFirstContactAcceptanceLoopbackRealCPaceRedemption(t *testing.T) {
	ctx := testContext(t)
	alice, bob := identity(t), identity(t)
	listener, err := firstcontact.Listen(firstcontact.Endpoint{Network: "tcp", Address: "127.0.0.1:0"}, alice, time.Now)
	mustOK(t, err)
	t.Cleanup(func() { listener.Close() })
	incoming := make(chan connResult, 1)
	go func() { c, e := listener.Accept(ctx); incoming <- connResult{c, e} }()
	client, err := firstcontact.Dial(ctx, firstcontact.Endpoint{Network: "tcp", Address: listener.Addr().String()}, bob, time.Now)
	mustOK(t, err)
	t.Cleanup(func() { client.Close() })
	r := <-incoming
	mustOK(t, r.err)
	t.Cleanup(func() { r.conn.Close() })
	store, joins := peer.NewMemPairingStore(), peer.NewMemJoinStore()
	invitations, err := peer.NewInvitations(peer.DefaultInvitationPolicy(), time.Now, rand.Reader, store)
	mustOK(t, err)
	invitation, err := invitations.Open()
	mustOK(t, err)
	inviter, err := peer.NewInviter(pake.New(), invitations, alice)
	mustOK(t, err)
	joiner, err := peer.NewJoiner(pake.New(), joins, bob, time.Now, rand.Reader)
	mustOK(t, err)
	done := make(chan error, 1)
	go func() { _, err := inviter.Serve(ctx, r.conn.Frames(ctx), r.conn.Connection()); done <- err }()
	row, err := joiner.Join(ctx, client.Frames(ctx), peer.JoinInput{Connection: client.Connection(), Code: []byte(invitation.Code), Name: "Bob"})
	mustOK(t, err)
	mustOK(t, <-done)
	equal(t, row.State, peer.JoinAwaitingAcceptance)
	equal(t, row.Peer, alice.Fingerprint())
	requests, err := store.Requests(ctx)
	mustOK(t, err)
	equal(t, len(requests), 1)
	equal(t, requests[0].IdentityKey, bob.Fingerprint())
	equal(t, requests[0].State, peer.PairingPending)
	outcome, found, err := store.Outcome(ctx, row.InviterOperationID)
	mustOK(t, err)
	equal(t, found, true)
	equal(t, bytes.Equal(row.TranscriptID, outcome.TranscriptID), true)
}

func TestFirstContactAcceptanceCancelledAcceptLeavesListenerUsable(t *testing.T) {
	alice, bob := identity(t), identity(t)
	ln, err := firstcontact.Listen(firstcontact.Endpoint{Network: "tcp", Address: "127.0.0.1:0"}, alice, time.Now)
	mustOK(t, err)
	t.Cleanup(func() { ln.Close() })
	ctx, cancel := context.WithCancel(testContext(t))
	done := make(chan error, 1)
	go func() { _, e := ln.Accept(ctx); done <- e }()
	select {
	case e := <-done:
		t.Fatalf("accept returned before cancellation: %v", e)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case e := <-done:
		equal(t, errors.Is(e, context.Canceled), true)
	case <-time.After(time.Second):
		t.Fatal("Accept ignored cancellation")
	}
	ctx = testContext(t)
	accepted := make(chan connResult, 1)
	go func() { c, e := ln.Accept(ctx); accepted <- connResult{c, e} }()
	c, err := firstcontact.Dial(ctx, firstcontact.Endpoint{Network: "tcp", Address: ln.Addr().String()}, bob, time.Now)
	mustOK(t, err)
	defer c.Close()
	r := <-accepted
	mustOK(t, r.err)
	defer r.conn.Close()
}

func TestFirstContactAcceptanceInvalidConfigurationRefused(t *testing.T) {
	id := identity(t)
	for _, network := range []string{"", "unix", "udp"} {
		ep := firstcontact.Endpoint{Network: network, Address: "127.0.0.1:0"}
		_, err := firstcontact.Listen(ep, id, time.Now)
		equal(t, errors.Is(err, firstcontact.ErrFirstContact), true)
		_, err = firstcontact.Dial(testContext(t), ep, id, time.Now)
		equal(t, errors.Is(err, firstcontact.ErrFirstContact), true)
	}
	ep := firstcontact.Endpoint{Network: "tcp", Address: "127.0.0.1:0"}
	_, err := firstcontact.Listen(ep, nil, time.Now)
	mustFail(t, err)
	_, err = firstcontact.Listen(ep, id, nil)
	mustFail(t, err)
	_, err = firstcontact.Dial(testContext(t), ep, nil, time.Now)
	mustFail(t, err)
	_, err = firstcontact.Dial(testContext(t), ep, id, nil)
	mustFail(t, err)
}
