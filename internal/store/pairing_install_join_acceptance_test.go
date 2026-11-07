package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func (f *operatorContactFixture) epochJoinReady(t *testing.T, id string) peer.JoinRecord {
	t.Helper()
	ctx := context.Background()
	_, err := f.db.BeginJoin(ctx, peer.JoinStart{OperationID: id, Joiner: joinKey(1), Name: "Joiner", At: joinAt})
	pairOK(t, err)
	_, err = f.db.SetCorrelation(ctx, id, peer.InviterHello{InvitationID: "inv-" + id, OperationID: "inviter-" + id}, joinAt)
	pairOK(t, err)
	r, err := f.db.RecordVerified(ctx, id, peer.ExchangeResult{Peer: f.contact, TranscriptID: []byte("epoch-join-transcript"), Mode: peer.ModeEpoch}, joinAt)
	pairOK(t, err)
	r, err = f.db.RecordReceipt(ctx, id, joinReceipt(r), joinAt)
	pairOK(t, err)
	return r
}

func TestPairingInstallAcceptanceJoinInstallsOnlyAfterDecisionConfirmation(t *testing.T) {
	f := installFixture(t)
	r := f.epochJoinReady(t, "join-one")
	_, err := f.db.RecordDecision(context.Background(), r.OperationID, peer.PairingAccepted, messageAt)
	pairOK(t, err)
	confirmed, err := f.db.PeerMessages().Confirmed(context.Background(), f.contact)
	pairOK(t, err)
	pairEqual(t, confirmed, false)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_contacts`), 0)
	installed, err := f.db.RecordDecisionConfirmed(context.Background(), r.OperationID, peer.PairingAccepted, messageAt)
	pairOK(t, err)
	if installed.DecisionConfirmedAt.IsZero() {
		t.Fatal("join confirmation was not recorded")
	}
	x := f.exchange(t, r.OperationID)
	pairEqual(t, x.Role, "joiner")
	pairEqual(t, x.State, "installed")
	pairEqual(t, x.InstalledEpoch, int64(1))
	confirmed, err = f.db.PeerMessages().Confirmed(context.Background(), f.contact)
	pairOK(t, err)
	pairEqual(t, confirmed, true)
	before := f.installAuthority(t)
	_, err = f.db.RecordDecisionConfirmed(context.Background(), r.OperationID, peer.PairingAccepted, messageAt)
	pairOK(t, err)
	if !reflect.DeepEqual(before, f.installAuthority(t)) {
		t.Fatal("join confirmation replay moved authority")
	}
}

func TestPairingInstallAcceptanceJoinCapturesMarkAndRefusesCrossedVerification(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	mark, err := f.db.PairingMark(ctx)
	pairOK(t, err)
	r, err := f.db.BeginJoin(ctx, peer.JoinStart{OperationID: "crossed-join", Joiner: joinKey(1), Name: "Joiner", At: joinAt})
	pairOK(t, err)
	pairEqual(t, r.BegunMark, mark)
	_, err = f.db.SetCorrelation(ctx, r.OperationID, peer.InviterHello{InvitationID: "crossed-inv", OperationID: "crossed-inviter"}, joinAt)
	pairOK(t, err)
	_, _, err = f.db.UnpairContact(ctx, unpairRequest(t, f, "crossed-join-unpair"))
	pairOK(t, err)
	before := f.installAuthority(t)
	_, err = f.db.RecordVerified(ctx, r.OperationID, peer.ExchangeResult{Peer: f.contact, TranscriptID: []byte("crossed-transcript"), Mode: peer.ModeEpoch}, messageAt)
	pairEqual(t, errors.Is(err, peer.ErrCrossedTransition), true)
	x := f.exchange(t, r.OperationID)
	pairEqual(t, x.State, "refused")
	pairEqual(t, x.Reason, "crossed_transition")
	if !reflect.DeepEqual(before, f.installAuthority(t)) {
		t.Fatal("crossed join verification changed joins or contact authority")
	}
}

func TestPairingInstallAcceptanceRetiredJoinCannotRecordOrConfirmDecision(t *testing.T) {
	f := installFixture(t)
	r := f.epochJoinReady(t, "retired-join")
	_, err := f.db.RecordDecision(context.Background(), r.OperationID, peer.PairingAccepted, messageAt)
	pairOK(t, err)
	_, err = f.db.RecordDecisionConfirmed(context.Background(), r.OperationID, peer.PairingAccepted, messageAt)
	pairOK(t, err)
	_, _, err = f.db.UnpairContact(context.Background(), unpairRequest(t, f, "retired-join-unpair"))
	pairOK(t, err)
	before := f.installAuthority(t)
	_, err = f.db.RecordDecision(context.Background(), r.OperationID, peer.PairingAccepted, messageAt)
	pairEqual(t, errors.Is(err, peer.ErrJoinRetired), true)
	_, err = f.db.RecordDecisionConfirmed(context.Background(), r.OperationID, peer.PairingAccepted, messageAt)
	pairEqual(t, errors.Is(err, peer.ErrJoinRetired), true)
	if !reflect.DeepEqual(before, f.installAuthority(t)) {
		t.Fatal("retired join decision changed authority or the join")
	}
}

func TestPairingInstallAcceptanceNewJoinPersistsLatestAuthorityMark(t *testing.T) {
	f := newOperatorContactFixture(t)
	_, _, err := f.db.UnpairContact(context.Background(), unpairRequest(t, f, "before-join"))
	pairOK(t, err)
	mark, err := f.db.PairingMark(context.Background())
	pairOK(t, err)
	if mark == 0 {
		t.Fatal("unpair did not move the mark")
	}
	r, err := f.db.BeginJoin(context.Background(), peer.JoinStart{OperationID: "fresh-mark-join", Joiner: joinKey(1), Name: "Joiner", At: joinAt})
	pairOK(t, err)
	pairEqual(t, r.BegunMark, mark)
	stored, found, err := f.db.GetJoin(context.Background(), r.OperationID)
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, stored.BegunMark, mark)
}

func TestPairingInstallAcceptanceJoinRepairIntentIsImmutable(t *testing.T) {
	f := installFixture(t)
	start := peer.JoinStart{OperationID: "intent-join", Joiner: joinKey(1), Name: "Joiner", At: joinAt}
	_, err := f.db.BeginJoin(context.Background(), start)
	pairOK(t, err)
	before := f.installAuthority(t)
	start.Repair = f.contact
	_, err = f.db.BeginJoin(context.Background(), start)
	pairEqual(t, errors.Is(err, peer.ErrJoinConflict), true)
	if !reflect.DeepEqual(before, f.installAuthority(t)) {
		t.Fatal("join replay changed its declared repair identity")
	}
}
