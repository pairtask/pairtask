//go:build unix

package firstcontact_test

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/pake"
)

// A byte-forwarding TCP relay leaves TLS intact, but an attacker terminating
// two TLS sessions must not pass the pairing exchange between those sessions.
func TestFirstContactAcceptanceTLSInterceptCannotRelayPAKE(t *testing.T) {
	alice, bob, mallory := identity(t), identity(t), identity(t)
	aliceConn, attackerAlice := connected(t, alice, mallory)
	attackerBob, bobConn := connected(t, mallory, bob)
	ctx := testContext(t)
	ioCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	store, joins := peer.NewMemPairingStore(), peer.NewMemJoinStore()
	inv, err := peer.NewInvitations(peer.DefaultInvitationPolicy(), time.Now, rand.Reader, store)
	mustOK(t, err)
	opened, err := inv.Open()
	mustOK(t, err)
	inviter, err := peer.NewInviter(pake.New(), inv, alice)
	mustOK(t, err)
	joiner, err := peer.NewJoiner(pake.New(), joins, bob, time.Now, rand.Reader)
	mustOK(t, err)
	relayDone := make(chan error, 2)
	go func() { relayDone <- relayFrames(attackerAlice.Frames(ioCtx), attackerBob.Frames(ioCtx)) }()
	go func() { relayDone <- relayFrames(attackerBob.Frames(ioCtx), attackerAlice.Frames(ioCtx)) }()
	inviteDone := make(chan error, 1)
	go func() {
		_, e := inviter.Serve(ctx, aliceConn.Frames(ioCtx), aliceConn.Connection())
		inviteDone <- e
		cancel()
	}()
	_, err = joiner.Join(ctx, bobConn.Frames(ioCtx), peer.JoinInput{Connection: bobConn.Connection(), Code: []byte(opened.Code), Name: "Bob"})
	mustFail(t, err)
	cancel()
	mustFail(t, <-inviteDone)
	<-relayDone
	<-relayDone
	requests, err := store.Requests(context.Background())
	mustOK(t, err)
	equal(t, len(requests), 0)
	rows, err := joins.Joins(context.Background())
	mustOK(t, err)
	equal(t, len(rows), 1)
	equal(t, rows[0].State, peer.JoinFailed)
	equal(t, len(rows[0].TranscriptID), 0)
}

func relayFrames(to, from peer.Frames) error {
	for {
		frame, err := from.Recv()
		if err != nil {
			return err
		}
		if err := to.Send(frame); err != nil {
			return err
		}
	}
}
