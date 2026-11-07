package store_test

import (
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestPairingRepairAcceptanceOriginalJoinerCanInviteDeclaredRepair(t *testing.T) {
	f := installFixture(t)
	ctx := context.Background()
	join := f.epochJoinReady(t, "original-joiner")
	_, err := f.db.RecordDecision(ctx, join.OperationID, peer.PairingAccepted, messageAt)
	pairOK(t, err)
	_, err = f.db.RecordDecisionConfirmed(ctx, join.OperationID, peer.PairingAccepted, messageAt)
	pairOK(t, err)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_pairing_requests`), 0)
	outcome := f.epochOutcome(t, 77)
	outcome.Repair = f.contact
	request, err := f.db.Commit(ctx, outcome)
	pairOK(t, err)
	pairEqual(t, request.State, peer.PairingPending)
	pairEqual(t, request.Kind, peer.KindRepair)
	confirmed, err := f.db.PeerMessages().Confirmed(ctx, f.contact)
	pairOK(t, err)
	pairEqual(t, confirmed, true)
	pairEqual(t, f.exchange(t, join.OperationID).State, peer.ExchangeInstalled)
	installed := f.deliverEpoch(t, outcome)
	pairEqual(t, installed.Changed, true)
	pairEqual(t, installed.Epoch, int64(2))
	pairEqual(t, f.exchange(t, join.OperationID).State, peer.ExchangeRetired)
}
