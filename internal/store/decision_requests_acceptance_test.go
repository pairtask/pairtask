package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func openDecisionStore(t *testing.T, path string) *store.Store {
	t.Helper()
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func persistDecisionAnchor(t *testing.T, db *store.Store, id, from, to string, seq uint64, durable bool) {
	t.Helper()
	now := time.Now().UTC()
	msg := protocol.Message{Type: protocol.TypeMsg, MessageID: id, Seq: seq, TS: &now,
		From: from, SessionID: from + "-session", To: to, Payload: []byte(`{"text":"needs a decision"}`), Durable: durable}
	if err := db.PersistMessage(context.Background(), msg, msg.SessionID,
		[]protocol.Recipient{{AgentID: to, SessionID: to + "-session"}}); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionRequestsAcceptanceAnchorMustBeDurableLocalAgentToOperator(t *testing.T) {
	db := openDecisionStore(t, filepath.Join(t.TempDir(), "decisions.db"))
	persistDecisionAnchor(t, db, "valid", "codex-1", protocol.OperatorID, 1, true)
	persistDecisionAnchor(t, db, "other-agent", "claude-1", protocol.OperatorID, 2, true)
	persistDecisionAnchor(t, db, "other-recipient", "codex-1", "claude-1", 3, true)
	persistDecisionAnchor(t, db, "non-durable", "codex-1", protocol.OperatorID, 4, false)
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"valid", true}, {"other-agent", false}, {"other-recipient", false},
		{"non-durable", false}, {"missing", false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			got, err := db.AnchorOwnedBy(context.Background(), tc.id, "codex-1")
			if err != nil || got != tc.want {
				t.Fatalf("anchor %q owned = %t, error %v; want %t", tc.id, got, err, tc.want)
			}
		})
	}
}

func TestDecisionRequestsAcceptanceKeyAndAnchorReplayPreserveFirstRecord(t *testing.T) {
	db := openDecisionStore(t, filepath.Join(t.TempDir(), "decisions.db"))
	ctx := context.Background()
	first, replayed, err := db.CreateDecision(ctx, store.Decision{
		ID: "d-first", Agent: "codex-1", Key: "ship", Anchor: "ask-1",
		Summary: "Approve release", Delivery: protocol.DeliveryPending,
	})
	if err != nil || replayed || first.ID != "d-first" {
		t.Fatalf("first decision = %+v, replayed %t, error %v", first, replayed, err)
	}
	for _, candidate := range []store.Decision{
		{ID: "d-second", Agent: "codex-1", Key: "ship", Anchor: "ask-2", Summary: "Changed summary", Delivery: protocol.DeliveryPending},
		{ID: "d-third", Agent: "codex-1", Key: "other", Anchor: "ask-1", Summary: "Another summary", Delivery: protocol.DeliveryPending},
	} {
		got, replayed, err := db.CreateDecision(ctx, candidate)
		if err != nil || !replayed || got.ID != first.ID || got.Anchor != first.Anchor || got.Summary != first.Summary {
			t.Fatalf("replay changed decision: %+v, replayed %t, error %v", got, replayed, err)
		}
	}
	listed, err := db.DecisionsSince(ctx, first.CreatedAt.Add(-time.Second), 10)
	if err != nil || len(listed) != 1 {
		t.Fatalf("replay left %d records, error %v", len(listed), err)
	}
}

func TestDecisionRequestsAcceptanceConcurrentSameKeyHasOneWinner(t *testing.T) {
	db := openDecisionStore(t, filepath.Join(t.TempDir(), "decisions.db"))
	type outcome struct {
		decision store.Decision
		replayed bool
		err      error
	}
	const writers = 8
	results := make(chan outcome, writers)
	start := make(chan struct{})
	var pending sync.WaitGroup
	for i := 0; i < writers; i++ {
		pending.Add(1)
		go func(i int) {
			defer pending.Done()
			<-start
			d, repeated, err := db.CreateDecision(context.Background(), store.Decision{
				ID: fmt.Sprintf("d-%d", i), Agent: "codex-1", Key: "one-release",
				Anchor: fmt.Sprintf("ask-%d", i), Summary: "Approve release", Delivery: protocol.DeliveryPending,
			})
			results <- outcome{d, repeated, err}
		}(i)
	}
	close(start)
	pending.Wait()
	close(results)
	winner := ""
	inserted := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent replay failed: %v", result.err)
		}
		if winner == "" {
			winner = result.decision.ID
		}
		if result.decision.ID != winner {
			t.Fatalf("same key produced %q and %q", winner, result.decision.ID)
		}
		if !result.replayed {
			inserted++
		}
	}
	if inserted != 1 {
		t.Fatalf("same key inserted %d decisions, want one", inserted)
	}
}

func TestDecisionRequestsAcceptancePendingSurvivesRestartButSentDoesNotRepost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.db")
	db := openDecisionStore(t, path)
	ctx := context.Background()
	for _, id := range []string{"pending", "sent"} {
		if _, _, err := db.CreateDecision(ctx, store.Decision{
			ID: id, Agent: "codex-1", Anchor: "ask-" + id,
			Summary: "Decision " + id, Delivery: protocol.DeliveryPending,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MarkDecisionDelivery(ctx, "sent", protocol.DeliverySent, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openDecisionStore(t, path)
	pending, err := db.PendingDecisions(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].ID != "pending" {
		t.Fatalf("pending after restart = %+v, error %v", pending, err)
	}
	answered, err := db.MarkDecisionAnswered(ctx, "ask-pending", time.Now())
	if err != nil || !answered {
		t.Fatalf("correlated answer = %t, error %v", answered, err)
	}
	answered, err = db.MarkDecisionAnswered(ctx, "unrelated", time.Now())
	if err != nil || answered {
		t.Fatalf("unrelated answer changed a request: %t, error %v", answered, err)
	}
	pending, err = db.PendingDecisions(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("answered/sent requests still pending: %+v, error %v", pending, err)
	}
}
