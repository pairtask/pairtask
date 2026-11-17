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

type legacyQueuedMessage struct {
	id, body string
	seal     []byte
}

func legacyQueue(t *testing.T, db *store.Store, m legacyQueuedMessage) {
	t.Helper()
	_, err := db.PeerMessages().Queue(context.Background(), messaging.Outgoing{
		Contact: sessionContact, ID: m.id, Text: m.body, Now: sessionAt, Seal: m.seal,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func legacyRow(t *testing.T, db *store.Store, count int, id string) messaging.Row {
	t.Helper()
	for _, row := range legacyUserRows(t, db, count) {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("row %q missing", id)
	return messaging.Row{}
}

// The migration assertions concern user messages. A legacy session can also
// queue pairtask's automatic signing-upgrade notice to that contact.
func legacyUserRows(t *testing.T, db *store.Store, want int) []messaging.Row {
	t.Helper()
	rows, _, err := db.PeerMessages().Page(context.Background(), sessionContact, 0, 64)
	if err != nil {
		t.Fatal(err)
	}
	users := make([]messaging.Row, 0, len(rows))
	for _, row := range rows {
		if row.From != messaging.SystemSender {
			users = append(users, row)
		}
	}
	if len(users) != want {
		t.Fatalf("user message rows = %d, want %d", len(users), want)
	}
	return users
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
	legacyQueue(t, f.a, legacyQueuedMessage{"a-to-old-b", "hello", []byte("sender seal")})
	legacyQueue(t, f.b, legacyQueuedMessage{id: "old-b-to-a", body: "old reply"})
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
	legacyQueue(t, f.b, legacyQueuedMessage{id: "downgraded-b-to-a", body: "back to v9"})
	sessionSuccess(t, sessionExchange(t, f.aBound, f.oldB, 0))
	row := legacyRow(t, f.b, 3, "downgraded-b-to-a")
	if row.State != messaging.StateRefused || row.Refusal.Reason != messaging.ReasonInvalid {
		t.Fatalf("v9 downgrade after v10 was not refused: %+v", row)
	}
	legacyUserRows(t, f.a, 2)
}

func (f legacyMigrationFixture) acceptDowngradeAfterSwitchOff(t *testing.T) {
	t.Helper()
	if err := f.a.SetRequireSeals(context.Background(), sessionContact, false, sessionAt); err != nil {
		t.Fatal(err)
	}
	legacyQueue(t, f.b, legacyQueuedMessage{id: "relaxed-b-to-a", body: "accepted unsigned"})
	sessionSuccess(t, sessionExchange(t, f.aBound, f.oldB, 0))
	assertUnsignedLegacyRow(t, legacyRow(t, f.a, 3, "relaxed-b-to-a"))
}
