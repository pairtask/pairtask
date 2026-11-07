package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestRetainedJoinAdmissionAcceptanceUnknownPeerRefusedBeforeRetainingIdentity(t *testing.T) {
	f := installFixture(t)
	f.installedEpoch(t, 101)
	seedContactCaps(t, f, 0, 2047)
	ctx := context.Background()
	_, err := f.db.BeginJoin(ctx, peer.JoinStart{OperationID: "retained-full-join", Joiner: joinKey(1), Name: "Joiner", At: joinAt})
	pairOK(t, err)
	_, err = f.db.SetCorrelation(ctx, "retained-full-join", peer.InviterHello{InvitationID: "capacity-inv", OperationID: "capacity-remote"}, joinAt)
	pairOK(t, err)
	before := f.installAuthority(t)
	_, err = f.db.RecordVerified(ctx, "retained-full-join", peer.ExchangeResult{Peer: joinKey(9006), TranscriptID: []byte("retained-capacity-transcript"), Mode: peer.ModeEpoch}, joinAt)
	pairEqual(t, errors.Is(err, peer.ErrIdentityStorage), true)
	if !reflect.DeepEqual(before, f.installAuthority(t)) {
		t.Fatal("refused join retained a new identity or changed authority")
	}
}
func TestRetainedJoinAdmissionAcceptanceKnownPeerReusesSlotAtFullRetention(t *testing.T) {
	f := installFixture(t)
	f.installedEpoch(t, 101)
	seedContactCaps(t, f, 0, 2047)
	f.contact = joinKey(10000)
	r := f.epochJoinReady(t, "retained-known-join")
	_, err := f.db.RecordDecision(context.Background(), r.OperationID, peer.PairingAccepted, messageAt)
	pairOK(t, err)
	_, err = f.db.RecordDecisionConfirmed(context.Background(), r.OperationID, peer.PairingAccepted, messageAt)
	pairOK(t, err)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_contacts`), 2048)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_contacts WHERE unpaired_at=''`), 2)
}
