package store_test

import (
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func TestSealLegacyContactStoreAcceptanceSeenSurvivesRepairAndRestart(t *testing.T) {
	f := installFixture(t)
	ctx := context.Background()
	f.installedEpoch(t, 101)
	assertSealSeen(t, f, false)
	before := pairingVersionBound(t, f)
	pairOK(t, before.SawVersion(ctx, messaging.SealVersion))
	assertSealSeen(t, f, true)

	// A repaired pairing can negotiate v9, but cannot reset the contact's
	// signing history or the operator's require-seals choice.
	next := f.epochOutcome(t, 102)
	next.Repair = f.contact
	_, err := f.db.Commit(ctx, next)
	pairOK(t, err)
	f.deliverEpoch(t, next)
	fresh := pairingVersionBound(t, f)
	pairOK(t, fresh.SawVersion(ctx, messaging.PairingVersion))
	assertSealSeen(t, f, true)
	pairOK(t, f.db.SetRequireSeals(ctx, f.contact, true, messageAt))
	assertLegacyRefusedAfterSeal(t, f)

	pairOK(t, f.db.Close())
	f.db = pairSQLite(t, f.path)
	assertSealSeen(t, f, true)
	assertLegacyRefusedAfterSeal(t, f)
}

func assertSealSeen(t *testing.T, f *operatorContactFixture, want bool) {
	t.Helper()
	states, err := f.db.ContactNoticeStates(context.Background())
	pairOK(t, err)
	state, ok := states[f.contact]
	if !ok || state.SealsSeen != want {
		t.Fatalf("contact signing history after repair/restart: present=%t state=%+v, want seen=%t", ok, state, want)
	}
}

func assertLegacyRefusedAfterSeal(t *testing.T, f *operatorContactFixture) {
	t.Helper()
	m := messaging.Message{ID: "old-after-upgrade", N: 1, CreatedAt: messageAt, Text: "old version"}
	a, err := f.db.PeerMessages().AcceptVersion(context.Background(), f.contact,
		messaging.Arrival{Version: int(messaging.PairingVersion), Message: m, At: messageAt})
	pairOK(t, err)
	if a.Stored || a.Reason != messaging.ReasonInvalid {
		t.Fatalf("downgraded contact was not refused: %+v", a)
	}
}
