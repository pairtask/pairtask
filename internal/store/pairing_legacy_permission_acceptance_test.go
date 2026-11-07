package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestPairingLegacyPermissionAcceptanceFirstPairStaysNullAndRecordsLegacy(t *testing.T) {
	f := installFixture(t)
	o := f.epochOutcome(t, 102)
	o.Mode = peer.ModeLegacy
	_, err := f.db.Commit(context.Background(), o)
	pairOK(t, err)
	x := f.exchange(t, o.OperationID)
	pairEqual(t, x.Mode, peer.ModeLegacy)
	pairEqual(t, x.State, "legacy")
	_, err = f.db.Accept(context.Background(), f.contact)
	pairOK(t, err)
	_, err = f.db.Deliver(context.Background(), f.contact, peer.PairingAccepted, messageAt)
	pairOK(t, err)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_contacts WHERE pairing_id IS NULL`), 1)
	confirmed, err := f.db.PeerMessages().Confirmed(context.Background(), f.contact)
	pairOK(t, err)
	pairEqual(t, confirmed, true)
}

func TestPairingLegacyPermissionAcceptanceActiveNullAddressMoveKeepsAuthority(t *testing.T) {
	f := newOperatorContactFixture(t)
	before := f.installAuthority(t)
	o := f.epochOutcome(t, 102)
	o.Mode = peer.ModeLegacy
	_, err := f.db.Commit(context.Background(), o)
	pairOK(t, err)
	x := f.exchange(t, o.OperationID)
	pairEqual(t, x.Mode, peer.ModeLegacy)
	pairEqual(t, x.Kind, peer.KindAddressMove)
	pairEqual(t, x.State, "legacy")
	after := f.installAuthority(t)
	for _, table := range []string{"peer_contacts", "pairing_marks", "peer_pairing_requests"} {
		if !reflect.DeepEqual(before[table], after[table]) {
			t.Fatalf("legacy address move changed %s", table)
		}
	}
}

func TestPairingLegacyPermissionAcceptanceInviterRefusesRetainedOrRepairIdentities(t *testing.T) {
	for _, kind := range []string{"unpaired", "remote-unpaired", "non-null", "retired-ledger", "declared-repair"} {
		t.Run(kind, func(t *testing.T) {
			f := legacyRefusalFixture(t, kind)
			o := f.epochOutcome(t, 102)
			o.Mode = peer.ModeLegacy
			if kind == "declared-repair" {
				o.Repair = f.contact
			}
			before := f.installAuthority(t)
			_, err := f.db.Commit(context.Background(), o)
			pairEqual(t, errors.Is(err, peer.ErrPeerTooOld), true)
			x := f.exchange(t, o.OperationID)
			pairEqual(t, x.Mode, peer.ModeLegacy)
			pairEqual(t, x.State, "refused")
			pairEqual(t, x.Reason, "peer_too_old")
			if !reflect.DeepEqual(before, f.installAuthority(t)) {
				t.Fatal("refused legacy commit changed authority or an outcome")
			}
		})
	}
}

func TestPairingLegacyPermissionAcceptanceJoinerRefusesBeforeVerifiedStateChanges(t *testing.T) {
	for _, kind := range []string{"unpaired", "remote-unpaired", "non-null", "retired-ledger", "declared-repair"} {
		t.Run(kind, func(t *testing.T) {
			f := legacyRefusalFixture(t, kind)
			ctx := context.Background()
			start := peer.JoinStart{OperationID: "legacy-permission-join", Joiner: joinKey(1), Name: "Joiner", At: joinAt}
			if kind == "declared-repair" {
				start.Repair = f.contact
			}
			_, err := f.db.BeginJoin(ctx, start)
			pairOK(t, err)
			_, err = f.db.SetCorrelation(ctx, start.OperationID, peer.InviterHello{InvitationID: "legacy-permission-inv", OperationID: "legacy-permission-inviter"}, joinAt)
			pairOK(t, err)
			before := f.installAuthority(t)
			_, err = f.db.RecordVerified(ctx, start.OperationID, peer.ExchangeResult{Peer: f.contact, TranscriptID: []byte("legacy-permission-transcript"), Mode: peer.ModeLegacy}, messageAt)
			pairEqual(t, errors.Is(err, peer.ErrPeerTooOld), true)
			x := f.exchange(t, start.OperationID)
			pairEqual(t, x.Mode, peer.ModeLegacy)
			pairEqual(t, x.State, "refused")
			pairEqual(t, x.Reason, "peer_too_old")
			if !reflect.DeepEqual(before, f.installAuthority(t)) {
				t.Fatal("legacy verification refusal changed the join or authority")
			}
		})
	}
}
