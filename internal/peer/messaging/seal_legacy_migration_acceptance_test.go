package messaging_test

import (
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// A's policy can be on while B still speaks v9. Once B has negotiated v10,
// returning to v9 must not silently regain the migration exception.
func TestSealLegacyMigrationAcceptanceUpgradeAndDowngrade(t *testing.T) {
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
	legacyB := legacySealPeer{bBound}

	// A can still send, and B's unsigned message arrives visibly unsigned.
	_, err = a.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: sessionContact, ID: "a-to-old-b", Text: "hello", Now: sessionAt, Seal: []byte("sender seal")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: sessionContact, ID: "old-b-to-a", Text: "old reply", Now: sessionAt})
	if err != nil {
		t.Fatal(err)
	}
	sessionSuccess(t, sessionExchange(t, aBound, legacyB, 0))
	for _, row := range sessionRows(t, a.PeerMessages(), 2) {
		switch row.ID {
		case "a-to-old-b":
			assertDowngradedSenderRow(t, row, []byte("sender seal"))
		case "old-b-to-a":
			assertUnsignedLegacyRow(t, row)
		default:
			t.Fatalf("unexpected row after old contact exchange: %+v", row)
		}
	}
	if rows := sessionRows(t, b.PeerMessages(), 2); rows[0].State == messaging.StateRefused || rows[1].State == messaging.StateRefused {
		t.Fatalf("migration refused traffic to or from old B: %+v", rows)
	}

	// A v10 session is the high-water mark, even if no message is exchanged.
	sessionSuccess(t, sessionExchange(t, aBound, bBound, 0))
	_, err = b.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: sessionContact, ID: "downgraded-b-to-a", Text: "back to v9", Now: sessionAt})
	if err != nil {
		t.Fatal(err)
	}
	sessionSuccess(t, sessionExchange(t, aBound, legacyB, 0))
	foundDowngrade := false
	for _, row := range sessionRows(t, b.PeerMessages(), 3) {
		if row.ID == "downgraded-b-to-a" {
			foundDowngrade = true
			if row.State != messaging.StateRefused || row.Refusal.Reason != messaging.ReasonInvalid {
				t.Fatalf("v9 downgrade after v10 was not refused: %+v", row)
			}
		}
	}
	if !foundDowngrade {
		t.Fatal("downgraded B's message disappeared without a refusal")
	}
	if rows := sessionRows(t, a.PeerMessages(), 2); len(rows) != 2 {
		t.Fatalf("downgraded message reached A: %+v", rows)
	}

	if err := a.SetRequireSeals(ctx, sessionContact, false, sessionAt); err != nil {
		t.Fatal(err)
	}
	_, err = b.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: sessionContact, ID: "relaxed-b-to-a", Text: "accepted unsigned", Now: sessionAt})
	if err != nil {
		t.Fatal(err)
	}
	sessionSuccess(t, sessionExchange(t, aBound, legacyB, 0))
	for _, row := range sessionRows(t, a.PeerMessages(), 3) {
		if row.ID == "relaxed-b-to-a" {
			assertUnsignedLegacyRow(t, row)
			return
		}
	}
	t.Fatal("turning require-seals off did not admit B's unsigned message")
}
