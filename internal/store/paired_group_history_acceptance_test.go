package store_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func TestPairedGroupHistoryAcceptanceLocalAudienceSurvivesHistoryAndReplay(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	for i := range 3 {
		id := "paired-send-" + string(rune('a'+i))
		_, err := f.db.OperatorContactSend(ctx, store.ContactSend{
			OperationID: id, Contact: f.contact, Text: "question " + id,
			Here:    []string{"home-b", "home-a"},
			AuditID: f.audit(t, contactAuditInput{"send", id}), Now: messageAt.Add(time.Duration(i) * time.Second),
		})
		pairOK(t, err)
	}

	assertPairedGroupHistory(t, f.db, 3)
	assertPairedGroupReplay(t, f.db, "home-a", 3)
	pairOK(t, f.db.Close())
	f.db = pairSQLite(t, f.path)
	assertPairedGroupHistory(t, f.db, 3)
	assertPairedGroupReplay(t, f.db, "home-b", 3)
}

func assertPairedGroupHistory(t *testing.T, db *store.Store, count int) {
	t.Helper()
	page, err := db.ReadOperatorHistory(context.Background(), store.HistoryQuery{Budget: 1 << 20})
	pairOK(t, err)
	if len(page.Entries) != count {
		t.Fatalf("history lost paired sends: got %d, want %d", len(page.Entries), count)
	}
	for _, entry := range page.Entries {
		msg := entry.Message
		if !strings.HasPrefix(msg.To, "group:paired-send-") || !slices.Equal(msg.Group, []string{"home-a", "home-b"}) {
			t.Fatalf("paired send became a group:<id> tab after history: to=%q members=%q", msg.To, msg.Group)
		}
		pairEqual(t, entry.Direction, "sent")
	}
}

func assertPairedGroupReplay(t *testing.T, db *store.Store, agent string, count int) {
	t.Helper()
	msgs, err := db.ClaimPendingFor(context.Background(), protocol.Recipient{AgentID: agent, SessionID: "reconnected"}, protocol.Version)
	pairOK(t, err)
	if len(msgs) != count {
		t.Fatalf("replay lost %s paired sends: got %d, want %d", agent, len(msgs), count)
	}
	for _, msg := range msgs {
		if !msg.Replayed || !slices.Equal(msg.Group, []string{"home-a", "home-b"}) {
			t.Fatalf("paired send lost members during replay to %s: %+v", agent, msg)
		}
	}
}
