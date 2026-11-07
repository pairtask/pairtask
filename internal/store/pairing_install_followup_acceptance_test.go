package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestPairingInstallFollowupAcceptanceRefusedRepairKeepsExactDeclaredIdentity(t *testing.T) {
	f := newOperatorContactFixture(t)
	original := f.epochOutcome(t, 101)
	original.Repair = joinKey(998)
	_, err := f.db.Commit(context.Background(), original)
	pairEqual(t, errors.Is(err, peer.ErrIdentityMismatch), true)
	before := f.exchange(t, original.OperationID)
	replay := original
	replay.Repair = joinKey(999)
	_, err = f.db.Commit(context.Background(), replay)
	pairEqual(t, errors.Is(err, peer.ErrOutcomeConflict), true)
	if !reflect.DeepEqual(before, f.exchange(t, original.OperationID)) {
		t.Fatal("different refused repair rewrote the original exchange")
	}
	_, err = f.db.Commit(context.Background(), original)
	pairEqual(t, errors.Is(err, peer.ErrIdentityMismatch), true)
}

func TestPairingInstallFollowupAcceptanceDeclaredRepairOfUnknownIdentityIsFirstPair(t *testing.T) {
	f := installFixture(t)
	o := f.epochOutcome(t, 101)
	o.Repair = f.contact
	request, err := f.db.Commit(context.Background(), o)
	pairOK(t, err)
	pairEqual(t, request.State, peer.PairingPending)
	pairEqual(t, request.Kind, peer.KindPair)
	pairEqual(t, f.exchange(t, o.OperationID).Kind, peer.KindPair)
	saved, found, err := f.db.Outcome(context.Background(), o.OperationID)
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, saved.Repair, o.Repair)
	_, err = f.db.Commit(context.Background(), o)
	pairOK(t, err)
	result := f.deliverEpoch(t, o)
	pairEqual(t, result.Changed, true)
	pairEqual(t, result.Epoch, int64(1))
}

func TestPairingInstallFollowupAcceptanceDeclaredRepairOfUnpairedIdentityNeedsFreshAcceptance(t *testing.T) {
	f := installFixture(t)
	f.installedEpoch(t, 101)
	_, _, err := f.db.UnpairContact(context.Background(), unpairRequest(t, f, "declared-after-unpair"))
	pairOK(t, err)
	o := f.epochOutcome(t, 102)
	o.Repair = f.contact
	request, err := f.db.Commit(context.Background(), o)
	pairOK(t, err)
	pairEqual(t, request.State, peer.PairingPending)
	pairEqual(t, request.Kind, peer.KindPair)
	result := f.deliverEpoch(t, o)
	pairEqual(t, result.Changed, true)
	pairEqual(t, result.Epoch, int64(2))
}

func TestPairingInstallFollowupAcceptanceRejectedRepairNeverReportsInstall(t *testing.T) {
	f := installFixture(t)
	old := f.installedEpoch(t, 101)
	before := f.exchange(t, old.OperationID)
	next := f.epochOutcome(t, 102)
	next.Repair = f.contact
	_, err := f.db.Commit(context.Background(), next)
	pairOK(t, err)
	_, err = f.db.Reject(context.Background(), f.contact)
	pairOK(t, err)
	authority := f.installAuthority(t)
	result, err := f.db.DeliverExchange(context.Background(), peer.ExchangeDelivery{IdentityKey: f.contact, OperationID: next.OperationID, Decision: peer.PairingRejected, At: messageAt})
	pairOK(t, err)
	pairEqual(t, result.Changed, false)
	pairEqual(t, result.Epoch, int64(0))
	pairEqual(t, result.Exchange.Reason, "rejected")
	pairEqual(t, len(result.PairingID), 0)
	after := f.installAuthority(t)
	for _, table := range []string{"peer_contacts", "pairing_marks"} {
		if !reflect.DeepEqual(authority[table], after[table]) {
			t.Fatalf("rejection changed %s", table)
		}
	}
	if !reflect.DeepEqual(before, f.exchange(t, old.OperationID)) {
		t.Fatal("rejected repair changed the installed exchange")
	}
	confirmed, err := f.db.PeerMessages().Confirmed(context.Background(), f.contact)
	pairOK(t, err)
	pairEqual(t, confirmed, true)
}

func TestPairingInstallFollowupAcceptanceEpochIDCannotBorrowLegacyDecisionAuthority(t *testing.T) {
	f := installFixture(t)
	f.installedEpoch(t, 101)
	_, err := f.raw.Exec(`UPDATE peer_contacts SET pairing_op='' WHERE identity_key=?`, f.contact)
	pairOK(t, err)
	confirmed, err := f.db.PeerMessages().Confirmed(context.Background(), f.contact)
	pairOK(t, err)
	pairEqual(t, confirmed, false)
}
