package store_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestPairingInstallAcceptanceEpochCandidateNeedsPinnedDelivery(t *testing.T) {
	f := installFixture(t)
	o := f.epochOutcome(t, 101)
	row, err := f.db.Commit(context.Background(), o)
	pairOK(t, err)
	pairEqual(t, row.State, peer.PairingPending)
	pairEqual(t, row.OperationID, o.OperationID)
	x := f.exchange(t, o.OperationID)
	pairEqual(t, x.State, "open")
	pairEqual(t, x.Kind, peer.KindPair)
	pairEqual(t, x.Mode, peer.ModeEpoch)
	pairEqual(t, x.BegunMark, o.BegunMark)
	before := f.installAuthority(t)
	_, err = f.db.Deliver(context.Background(), f.contact, peer.PairingAccepted, messageAt)
	pairEqual(t, errors.Is(err, peer.ErrOperationRequired), true)
	if !reflect.DeepEqual(before, f.installAuthority(t)) {
		t.Fatal("identity-only delivery changed an epoch candidate")
	}
}

func TestPairingInstallAcceptanceFreshInstallAndReplayAreStable(t *testing.T) {
	f := installFixture(t)
	o := f.epochOutcome(t, 101)
	_, err := f.db.Commit(context.Background(), o)
	pairOK(t, err)
	first := f.deliverEpoch(t, o)
	pairEqual(t, first.Changed, true)
	pairEqual(t, first.Epoch, int64(1))
	pairEqual(t, len(first.PairingID), 32)
	pairEqual(t, hex.EncodeToString(first.PairingID), "25ec9f078efb83649742016e288d105f3fe8b8e879768556df358c7574237619")
	pairEqual(t, first.Exchange.OperationID, o.OperationID)
	pairEqual(t, first.Exchange.State, "installed")
	pairEqual(t, first.Exchange.InstalledEpoch, int64(1))
	confirmed, err := f.db.PeerMessages().Confirmed(context.Background(), f.contact)
	pairOK(t, err)
	pairEqual(t, confirmed, true)
	before := f.installAuthority(t)
	ledger := f.exchange(t, o.OperationID)
	again, err := f.db.DeliverExchange(context.Background(), peer.ExchangeDelivery{IdentityKey: f.contact, OperationID: o.OperationID, Decision: peer.PairingAccepted, At: messageAt.Add(time.Hour)})
	pairOK(t, err)
	pairEqual(t, again.Changed, false)
	pairEqual(t, again.Epoch, first.Epoch)
	pairEqual(t, bytes.Equal(again.PairingID, first.PairingID), true)
	if !reflect.DeepEqual(ledger, f.exchange(t, o.OperationID)) {
		t.Fatal("installed replay rewrote immutable ledger")
	}
	if !reflect.DeepEqual(before, f.installAuthority(t)) {
		t.Fatal("installed replay moved authority, generation or mark")
	}
}

func TestPairingInstallAcceptanceLedgerAuthorityIgnoresDecisionMirror(t *testing.T) {
	f := installFixture(t)
	o := f.installedEpoch(t, 101)
	_, err := f.raw.Exec(`UPDATE peer_pairing_requests SET state='pending_acceptance',delivered_at=NULL WHERE identity_key=?`, f.contact)
	pairOK(t, err)
	confirmed, err := f.db.PeerMessages().Confirmed(context.Background(), f.contact)
	pairOK(t, err)
	pairEqual(t, confirmed, true)
	_, err = f.raw.Exec(`UPDATE pairing_exchanges SET state='retired' WHERE operation_id=?`, o.OperationID)
	pairOK(t, err)
	confirmed, err = f.db.PeerMessages().Confirmed(context.Background(), f.contact)
	pairOK(t, err)
	pairEqual(t, confirmed, false)
}

func TestPairingInstallAcceptanceCrossedCommitPersistsOnlyRefusal(t *testing.T) {
	f := newOperatorContactFixture(t)
	stale := f.epochOutcome(t, 101)
	_, _, err := f.db.UnpairContact(context.Background(), unpairRequest(t, f, "crossed-unpair"))
	pairOK(t, err)
	before := f.installAuthority(t)
	_, err = f.db.Commit(context.Background(), stale)
	pairEqual(t, errors.Is(err, peer.ErrCrossedTransition), true)
	x := f.exchange(t, stale.OperationID)
	pairEqual(t, x.State, "refused")
	pairEqual(t, x.Reason, "crossed_transition")
	_, err = f.db.Commit(context.Background(), stale)
	pairEqual(t, errors.Is(err, peer.ErrCrossedTransition), true)
	_, err = f.db.DeliverExchange(context.Background(), peer.ExchangeDelivery{IdentityKey: f.contact, OperationID: stale.OperationID, Decision: peer.PairingAccepted, At: messageAt})
	pairEqual(t, errors.Is(err, peer.ErrCrossedTransition), true)
	if !reflect.DeepEqual(x, f.exchange(t, stale.OperationID)) {
		t.Fatal("refusal replay rewrote its recorded exchange")
	}
	if !reflect.DeepEqual(before, f.installAuthority(t)) {
		t.Fatal("crossed commit wrote authority or a candidate besides its refusal ledger")
	}
}

func TestPairingInstallAcceptanceAmbiguousCandidatesRefuseBoth(t *testing.T) {
	f := installFixture(t)
	a, b := f.epochOutcome(t, 101), f.epochOutcome(t, 102)
	_, err := f.db.Commit(context.Background(), a)
	pairOK(t, err)
	_, err = f.db.Commit(context.Background(), b)
	pairEqual(t, errors.Is(err, peer.ErrAmbiguousPairing), true)
	for _, op := range []string{a.OperationID, b.OperationID} {
		x := f.exchange(t, op)
		pairEqual(t, x.State, "refused")
		pairEqual(t, x.Reason, "ambiguous")
	}
	confirmed, err := f.db.PeerMessages().Confirmed(context.Background(), f.contact)
	pairOK(t, err)
	pairEqual(t, confirmed, false)
}

func TestPairingInstallAcceptanceRepairCannotAuthenticateAnotherIdentity(t *testing.T) {
	f := newOperatorContactFixture(t)
	o := f.epochOutcome(t, 101)
	o.Repair = joinKey(999)
	before := f.installAuthority(t)
	_, err := f.db.Commit(context.Background(), o)
	pairEqual(t, errors.Is(err, peer.ErrIdentityMismatch), true)
	x := f.exchange(t, o.OperationID)
	pairEqual(t, x.State, "refused")
	pairEqual(t, x.Reason, "identity_mismatch")
	if !reflect.DeepEqual(before, f.installAuthority(t)) {
		t.Fatal("mismatched repair changed the authenticated identity's authority")
	}
}

func TestPairingInstallAcceptanceLegacyDeliverRemainsUsable(t *testing.T) {
	f := installFixture(t)
	o := pairOutcome(101)
	o.IdentityKey = f.contact
	_, err := f.db.Commit(context.Background(), o)
	pairOK(t, err)
	_, err = f.db.Accept(context.Background(), f.contact)
	pairOK(t, err)
	_, err = f.db.Deliver(context.Background(), f.contact, peer.PairingAccepted, messageAt)
	pairOK(t, err)
	confirmed, err := f.db.PeerMessages().Confirmed(context.Background(), f.contact)
	pairOK(t, err)
	pairEqual(t, confirmed, true)
}

func TestPairingInstallAcceptanceAmbiguousRepairsKeepInstalledAuthority(t *testing.T) {
	f := installFixture(t)
	old := f.installedEpoch(t, 101)
	first, second := f.epochOutcome(t, 102), f.epochOutcome(t, 103)
	first.Repair, second.Repair = f.contact, f.contact
	before := f.exchange(t, old.OperationID)
	_, err := f.db.Commit(context.Background(), first)
	pairOK(t, err)
	_, err = f.db.Commit(context.Background(), second)
	pairEqual(t, errors.Is(err, peer.ErrAmbiguousPairing), true)
	for _, op := range []string{first.OperationID, second.OperationID} {
		x := f.exchange(t, op)
		pairEqual(t, x.State, "refused")
		pairEqual(t, x.Reason, "ambiguous")
	}
	if !reflect.DeepEqual(before, f.exchange(t, old.OperationID)) {
		t.Fatal("ambiguous repairs retired installed authority")
	}
	confirmed, err := f.db.PeerMessages().Confirmed(context.Background(), f.contact)
	pairOK(t, err)
	pairEqual(t, confirmed, true)
}

func TestPairingInstallAcceptanceOldCommitReplayDoesNotReplaceCandidateMirror(t *testing.T) {
	f := installFixture(t)
	old := f.installedEpoch(t, 101)
	next := f.epochOutcome(t, 102)
	next.Repair = f.contact
	_, err := f.db.Commit(context.Background(), next)
	pairOK(t, err)
	before := f.installAuthority(t)
	_, err = f.db.Commit(context.Background(), old)
	pairOK(t, err)
	if !reflect.DeepEqual(before, f.installAuthority(t)) {
		t.Fatal("old commit replay moved the candidate or authority")
	}
	request, found, err := f.db.Request(context.Background(), f.contact)
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, request.OperationID, next.OperationID)
}
