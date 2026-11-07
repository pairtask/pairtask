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

func pairingWireRepair(t *testing.T, c pairingTLSConnections, s pairingWireStack) pairingWireStack {
	t.Helper()
	invitations, err := peer.NewInvitations(peer.DefaultInvitationPolicy(), time.Now, rand.Reader, s.alice)
	mustOK(t, err)
	opened, err := invitations.OpenRepair(c.bob.Fingerprint())
	mustOK(t, err)
	equal(t, opened.Repair, c.bob.Fingerprint())
	inviter, err := peer.NewInviter(pake.New(), invitations, c.alice)
	mustOK(t, err)
	ctx := testContext(t)
	pending, _, err := s.joiner.Begin(ctx, peer.JoinRequest{OperationID: "wire-repair-join", Name: "Bob", Code: []byte(opened.Code), Repair: c.alice.Fingerprint()})
	mustOK(t, err)
	if pending == nil {
		t.Fatal("repair join intent missing")
	}
	done := make(chan error, 1)
	go func() { _, err := inviter.Serve(ctx, c.server.Frames(ctx), c.server.Connection()); done <- err }()
	joined, err := pending.Run(ctx, c.client.Frames(ctx), c.client.Connection())
	mustOK(t, err)
	mustOK(t, <-done)
	return pairingWireStack{inviter, s.joiner, s.alice, s.bob, joined}
}

func TestPairingRepairWireAcceptanceBothLocalIntentsSurviveRealExchange(t *testing.T) {
	first := pairingProtocolEndpoints(t, pairingTLSOptions{})
	original := pairingWireJoin(t, first)
	original.settle(t, first)
	second := first.reconnect(t, pairingTLSOptions{})
	repaired := pairingWireRepair(t, second, original)
	ctx := context.Background()
	outcome, found, err := repaired.alice.Outcome(ctx, repaired.joined.InviterOperationID)
	mustOK(t, err)
	equal(t, found, true)
	equal(t, outcome.Repair, first.bob.Fingerprint())
	equal(t, outcome.Mode, peer.ModeEpoch)
	equal(t, repaired.joined.Repair, first.alice.Fingerprint())
	equal(t, repaired.joined.Mode, peer.ModeEpoch)
	inviteLedger, found, err := repaired.alice.Exchange(ctx, repaired.joined.InviterOperationID)
	mustOK(t, err)
	equal(t, found, true)
	equal(t, inviteLedger.Kind, peer.KindRepair)
	joinLedger, found, err := repaired.bob.Exchange(ctx, repaired.joined.OperationID)
	mustOK(t, err)
	equal(t, found, true)
	equal(t, joinLedger.Kind, peer.KindRepair)
	repaired.settle(t, second)
	inviteLedger, _, err = repaired.alice.Exchange(ctx, repaired.joined.InviterOperationID)
	mustOK(t, err)
	joinLedger, _, err = repaired.bob.Exchange(ctx, repaired.joined.OperationID)
	mustOK(t, err)
	equal(t, inviteLedger.InstalledEpoch, int64(2))
	equal(t, joinLedger.InstalledEpoch, int64(2))
}
