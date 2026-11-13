package messaging_test

import (
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

type legacyMigrationFixture struct {
	a, b           *store.Store
	aBound, bBound *store.PeerMessages
	oldB           legacySealPeer
}

func newLegacyMigrationFixture(t *testing.T) legacyMigrationFixture {
	t.Helper()
	a, _ := sessionFixture(t)
	b, _ := sessionFixture(t)
	ctx := context.Background()
	if err := a.SetRequireSeals(ctx, sessionContact, true, sessionAt); err != nil {
		t.Fatal(err)
	}
	aBound, err := a.PeerMessages().Admit(ctx, sessionContact)
	if err != nil {
		t.Fatal(err)
	}
	bBound, err := b.PeerMessages().Admit(ctx, sessionContact)
	if err != nil {
		t.Fatal(err)
	}
	return legacyMigrationFixture{a, b, aBound, bBound, legacySealPeer{bBound}}
}

func legacyQueue(t *testing.T, db *store.Store, id, body string, seal []byte) {
	t.Helper()
	_, err := db.PeerMessages().Queue(context.Background(), messaging.Outgoing{
		Contact: sessionContact, ID: id, Text: body, Now: sessionAt, Seal: seal,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func legacyRow(t *testing.T, db *store.Store, count int, id string) messaging.Row {
	t.Helper()
	for _, row := range sessionRows(t, db.PeerMessages(), count) {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("row %q missing", id)
	return messaging.Row{}
}

// A's policy can be on while B still speaks v9. Once B has negotiated v10,
// returning to v9 must not silently regain the migration exception.
func TestSealLegacyMigrationAcceptanceUpgradeAndDowngrade(t *testing.T) {
	f := newLegacyMigrationFixture(t)
	f.acceptOldContactBothDirections(t)
	f.upgradeWithoutMessage(t)
	f.refuseLaterDowngrade(t)
	f.acceptDowngradeAfterSwitchOff(t)
}

func (f legacyMigrationFixture) acceptOldContactBothDirections(t *testing.T) {
	t.Helper()
	legacyQueue(t, f.a, "a-to-old-b", "hello", []byte("sender seal"))
	legacyQueue(t, f.b, "old-b-to-a", "old reply", nil)
	sessionSuccess(t, sessionExchange(t, f.aBound, f.oldB, 0))
	assertDowngradedSenderRow(t, legacyRow(t, f.a, 2, "a-to-old-b"), []byte("sender seal"))
	assertUnsignedLegacyRow(t, legacyRow(t, f.a, 2, "old-b-to-a"))
	if row := legacyRow(t, f.b, 2, "old-b-to-a"); row.State != messaging.StateDelivered {
		t.Fatalf("old contact's reply was refused: %+v", row)
	}
	assertUnsignedLegacyRow(t, legacyRow(t, f.b, 2, "a-to-old-b"))
}

func (f legacyMigrationFixture) upgradeWithoutMessage(t *testing.T) {
	t.Helper()
	// A v10 session is the high-water mark even without a message.
	sessionSuccess(t, sessionExchange(t, f.aBound, f.bBound, 0))
}

func (f legacyMigrationFixture) refuseLaterDowngrade(t *testing.T) {
	t.Helper()
	legacyQueue(t, f.b, "downgraded-b-to-a", "back to v9", nil)
	sessionSuccess(t, sessionExchange(t, f.aBound, f.oldB, 0))
	row := legacyRow(t, f.b, 3, "downgraded-b-to-a")
	if row.State != messaging.StateRefused || row.Refusal.Reason != messaging.ReasonInvalid {
		t.Fatalf("v9 downgrade after v10 was not refused: %+v", row)
	}
	sessionRows(t, f.a.PeerMessages(), 2)
}

func (f legacyMigrationFixture) acceptDowngradeAfterSwitchOff(t *testing.T) {
	t.Helper()
	if err := f.a.SetRequireSeals(context.Background(), sessionContact, false, sessionAt); err != nil {
		t.Fatal(err)
	}
	legacyQueue(t, f.b, "relaxed-b-to-a", "accepted unsigned", nil)
	sessionSuccess(t, sessionExchange(t, f.aBound, f.oldB, 0))
	assertUnsignedLegacyRow(t, legacyRow(t, f.a, 3, "relaxed-b-to-a"))
}
