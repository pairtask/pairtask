package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func legacyHistoryWithoutContact(t *testing.T, kind string) *operatorContactFixture {
	t.Helper()
	f := installFixture(t)
	o := f.epochOutcome(t, 101)
	switch kind {
	case "legacy":
		o.Mode = peer.ModeLegacy
	case "refused":
		o.Repair = joinKey(999)
	}
	_, err := f.db.Commit(context.Background(), o)
	if kind == "refused" {
		pairEqual(t, errors.Is(err, peer.ErrIdentityMismatch), true)
	} else {
		pairOK(t, err)
	}
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_contacts`), 0)
	return f
}

func TestPairingLegacyHistoryAcceptanceInviterRequiresGenuinelyNewIdentity(t *testing.T) {
	for _, kind := range []string{"open", "legacy", "refused"} {
		t.Run(kind, func(t *testing.T) {
			f := legacyHistoryWithoutContact(t, kind)
			before := f.installAuthority(t)
			o := f.epochOutcome(t, 102)
			o.Mode = peer.ModeLegacy
			_, err := f.db.Commit(context.Background(), o)
			pairEqual(t, errors.Is(err, peer.ErrPeerTooOld), true)
			x := f.exchange(t, o.OperationID)
			pairEqual(t, x.State, "refused")
			pairEqual(t, x.Reason, "peer_too_old")
			if !reflect.DeepEqual(before, f.installAuthority(t)) {
				t.Fatal("legacy exchange with retained history changed authority or a candidate")
			}
		})
	}
}

func TestPairingLegacyHistoryAcceptanceJoinerRequiresGenuinelyNewIdentity(t *testing.T) {
	for _, kind := range []string{"open", "legacy", "refused"} {
		t.Run(kind, func(t *testing.T) {
			f := legacyHistoryWithoutContact(t, kind)
			ctx := context.Background()
			start := peer.JoinStart{OperationID: "history-legacy-join", Joiner: joinKey(1), Name: "Joiner", At: joinAt}
			_, err := f.db.BeginJoin(ctx, start)
			pairOK(t, err)
			_, err = f.db.SetCorrelation(ctx, start.OperationID, peer.InviterHello{InvitationID: "history-legacy-inv", OperationID: "history-legacy-inviter"}, joinAt)
			pairOK(t, err)
			before := f.installAuthority(t)
			_, err = f.db.RecordVerified(ctx, start.OperationID, peer.ExchangeResult{Peer: f.contact, TranscriptID: []byte("history-legacy-transcript"), Mode: peer.ModeLegacy}, messageAt)
			pairEqual(t, errors.Is(err, peer.ErrPeerTooOld), true)
			x := f.exchange(t, start.OperationID)
			pairEqual(t, x.State, "refused")
			pairEqual(t, x.Reason, "peer_too_old")
			if !reflect.DeepEqual(before, f.installAuthority(t)) {
				t.Fatal("legacy verification with retained history changed the join or authority")
			}
		})
	}
}
