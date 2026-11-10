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

type decisionAnchorCase struct {
	id, from, to string
	seq          uint64
	durable      bool
}

func persistDecisionAnchor(t *testing.T, db *store.Store, sample decisionAnchorCase) {
	t.Helper()
	now := time.Now().UTC()
	msg := protocol.Message{Type: protocol.TypeMsg, MessageID: sample.id, Seq: sample.seq, TS: &now,
		From: sample.from, SessionID: sample.from + "-session", To: sample.to,
		Payload: []byte(`{"text":"needs a decision"}`), Durable: sample.durable}
	if err := db.PersistMessage(context.Background(), msg, msg.SessionID,
		[]protocol.Recipient{{AgentID: sample.to, SessionID: sample.to + "-session"}}); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionRequestsAcceptanceAnchorMustBeDurableLocalAgentToOperator(t *testing.T) {
	db := openDecisionStore(t, filepath.Join(t.TempDir(), "decisions.db"))
	for _, sample := range []decisionAnchorCase{
		{"valid", "codex-1", protocol.OperatorID, 1, true},
		{"other-agent", "claude-1", protocol.OperatorID, 2, true},
		{"other-recipient", "codex-1", "claude-1", 3, true},
		{"non-durable", "codex-1", protocol.OperatorID, 4, false},
	} {
		persistDecisionAnchor(t, db, sample)
	}
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"valid", true}, {"other-agent", false}, {"other-recipient", false},
		{"non-durable", false}, {"missing", false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			got, err := db.AnchorOwnedBy(context.Background(), tc.id, "codex-1")
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
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
	assertFirstStoredDecision(t, first, replayed, err)
	for _, candidate := range []store.Decision{
		{ID: "d-second", Agent: "codex-1", Key: "ship", Anchor: "ask-2", Summary: "Changed summary", Delivery: protocol.DeliveryPending},
		{ID: "d-third", Agent: "codex-1", Key: "other", Anchor: "ask-1", Summary: "Another summary", Delivery: protocol.DeliveryPending},
	} {
		got, replayed, err := db.CreateDecision(ctx, candidate)
		assertStoredReplay(t, first, createDecisionOutcome{got, replayed, err})
	}
	listed, err := db.DecisionsSince(ctx, first.CreatedAt.Add(-time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("replay left %d records, error %v", len(listed), err)
	}
}

func assertFirstStoredDecision(t *testing.T, first store.Decision, replayed bool, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if replayed {
		t.Fatalf("first decision = %+v, replayed %t", first, replayed)
	}
	if first.ID != "d-first" {
		t.Fatalf("first decision = %+v, replayed %t", first, replayed)
	}
}

type createDecisionOutcome struct {
	decision store.Decision
	replayed bool
	err      error
}

func assertStoredReplay(t *testing.T, first store.Decision, outcome createDecisionOutcome) {
	t.Helper()
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	if !outcome.replayed {
		t.Fatalf("replay inserted a second decision: %+v", outcome.decision)
	}
	if outcome.decision.ID != first.ID {
		t.Fatalf("replay changed ID: %+v", outcome.decision)
	}
	if outcome.decision.Anchor != first.Anchor {
		t.Fatalf("replay changed anchor: %+v", outcome.decision)
	}
	if outcome.decision.Summary != first.Summary {
		t.Fatalf("replay changed summary: %+v", outcome.decision)
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
	createPendingDecisionRows(t, db)
	markSentDecision(t, db, "sent")
	db = reopenDecisionStore(t, db, path)
	assertPendingDecisionIDs(t, db, "pending")
	assertDecisionAnswer(t, db, "ask-pending", true)
	assertDecisionAnswer(t, db, "unrelated", false)
	assertPendingDecisionIDs(t, db)
}

func createPendingDecisionRows(t *testing.T, db *store.Store) {
	t.Helper()
	for _, id := range []string{"pending", "sent"} {
		if _, _, err := db.CreateDecision(context.Background(), store.Decision{
			ID: id, Agent: "codex-1", Anchor: "ask-" + id,
			Summary: "Decision " + id, Delivery: protocol.DeliveryPending,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func markSentDecision(t *testing.T, db *store.Store, id string) {
	t.Helper()
	if err := db.MarkDecisionDelivery(context.Background(), store.DeliveryMark{ID: id, Delivery: protocol.DeliverySent, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func reopenDecisionStore(t *testing.T, db *store.Store, path string) *store.Store {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return openDecisionStore(t, path)
}

func assertDecisionAnswer(t *testing.T, db *store.Store, anchor string, want bool) {
	t.Helper()
	answered, err := db.MarkDecisionAnswered(context.Background(), anchor, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if answered != want {
		t.Fatalf("answer for %q = %t, want %t", anchor, answered, want)
	}
}

func assertPendingDecisionIDs(t *testing.T, db *store.Store, want ...string) {
	t.Helper()
	pending, err := db.PendingDecisions(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != len(want) {
		t.Fatalf("pending after restart = %+v, want %v", pending, want)
	}
	for i, id := range want {
		if pending[i].ID != id {
			t.Fatalf("pending after restart = %+v, want %v", pending, want)
		}
	}
}
