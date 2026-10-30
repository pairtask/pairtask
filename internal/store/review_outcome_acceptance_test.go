package store_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func outcomeAsk(t *testing.T, db *store.Store, contact string) messaging.ReviewOutcome {
	t.Helper()
	m := remoteAsk("ask")
	_, err := db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: contact, ID: m.ID, Text: m.Text, To: m.To, Review: m.Review, Now: messageAt})
	pairOK(t, err)
	return messaging.ReviewOutcome{AskID: m.ID, Reviewer: m.To[0], Seq: 1, State: "requested"}
}

func TestReviewOutcomeAcceptanceIdentityAndSequence(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	o := outcomeAsk(t, db, contact)
	wrong := o
	wrong.Reviewer = "other"
	a, err := db.PeerMessages().AcceptOutcome(ctx, contact, wrong, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, false)
	a, err = db.PeerMessages().AcceptOutcome(ctx, contact, o, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	a, err = db.PeerMessages().AcceptOutcome(ctx, contact, o, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	wrong = o
	wrong.Summary = "different"
	a, err = db.PeerMessages().AcceptOutcome(ctx, contact, wrong, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Reason, messaging.ReasonConflict)
	o.Seq, o.Attempt, o.State = 3, 1, "accepted"
	a, err = db.PeerMessages().AcceptOutcome(ctx, contact, o, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	o.Seq, o.State = 2, "requested"
	a, err = db.PeerMessages().AcceptOutcome(ctx, contact, o, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	got, found, err := db.PeerMessages().AskOutFor(ctx, contact, o.AskID)
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, got.State, "accepted")
	pairEqual(t, got.Seq, uint64(3))
}

func TestReviewOutcomeAcceptanceRejectsImpossibleStates(t *testing.T) {
	for _, scenario := range []string{"accepted-no-attempt", "published-no-attempt", "completed-without-verdict", "oversize-summary", "foreign-local-state"} {
		t.Run(scenario, func(t *testing.T) {
			db, contact := messageFixture(t)
			o := outcomeAsk(t, db, contact)
			switch scenario {
			case "accepted-no-attempt":
				o.State = "accepted"
			case "published-no-attempt":
				o.State, o.Intent, o.Verdict = "published", 1, "approve"
			case "completed-without-verdict":
				o.State, o.Attempt = "completed", 1
			case "oversize-summary":
				o.Summary = strings.Repeat("x", messaging.OutcomeSummaryBytes+1)
			case "foreign-local-state":
				o.State = "contact_removed"
			}
			a, err := db.PeerMessages().AcceptOutcome(context.Background(), contact, o, messageAt)
			pairOK(t, err)
			if a.Stored {
				t.Fatalf("accepted invalid outcome in %s", scenario)
			}
		})
	}
}

func TestReviewOutcomeAcceptanceDelayedAckCannotDropLatest(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	reviewGrant(t, db, contact, store.ReviewAsk)
	m := remoteAsk("incoming")
	_, err := db.PeerMessages().Accept(ctx, contact, m, messageAt)
	pairOK(t, err)
	rows, err := db.PeerMessages().PendingOutcomes(ctx, contact, 10)
	pairOK(t, err)
	pairEqual(t, len(rows), 1)
	first := rows[0]
	_, err = db.DecideRemoteReview(ctx, store.RemoteDecision{Contact: contact, RemoteID: m.ID, Approve: false, Now: messageAt})
	pairOK(t, err)
	pairOK(t, db.PeerMessages().OutcomeAnswered(ctx, contact, messaging.OutcomeReply{AskID: m.ID, Seq: first.Seq}))
	rows, err = db.PeerMessages().PendingOutcomes(ctx, contact, 10)
	pairOK(t, err)
	pairEqual(t, len(rows), 1)
	pairEqual(t, rows[0].State, "declined")
	pairOK(t, db.PeerMessages().OutcomeAnswered(ctx, contact, messaging.OutcomeReply{AskID: m.ID, Seq: rows[0].Seq + 100}))
	rows, err = db.PeerMessages().PendingOutcomes(ctx, contact, 10)
	pairOK(t, err)
	pairEqual(t, len(rows), 1)
}

func queuedAgentAsk(t *testing.T, f workspaceStoreFixture) messaging.ReviewOutcome {
	t.Helper()
	w := f.request("ask")
	w.Review = remoteAsk("ask").Review
	_, err := f.db.QueueWorkspaceSend(context.Background(), w)
	pairOK(t, err)
	return messaging.ReviewOutcome{AskID: "ask", Reviewer: "target", Seq: 1, State: "requested"}
}

func TestReviewOutcomeAcceptanceNoticeFailureRollsBackAnchor(t *testing.T) {
	f := workspaceStore(t)
	ctx := context.Background()
	o := queuedAgentAsk(t, f)
	_, err := f.raw.Exec("CREATE TRIGGER fail_outcome_notice BEFORE INSERT ON remote_imports BEGIN SELECT RAISE(ABORT, 'injected notice failure'); END")
	pairOK(t, err)
	_, err = f.db.PeerMessages().AcceptOutcome(ctx, f.key, o, messageAt)
	if err == nil {
		t.Fatal("notice failure unexpectedly succeeded")
	}
	held, _, err := f.db.PeerMessages().AskOutFor(ctx, f.key, o.AskID)
	pairOK(t, err)
	pairEqual(t, held.Seq, uint64(0))
	_, err = f.raw.Exec("DROP TRIGGER fail_outcome_notice")
	pairOK(t, err)
	a, err := f.db.PeerMessages().AcceptOutcome(ctx, f.key, o, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
}

func TestReviewOutcomeAcceptanceCancellationKeepsExactIntentFence(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	o := outcomeAsk(t, db, contact)
	o.Attempt, o.Intent, o.Verdict = 1, 1, "comment"
	for i, state := range []string{"publishing", "cancelled", "uncertain"} {
		o.Seq, o.State = uint64(i+1), state
		a, err := db.PeerMessages().AcceptOutcome(ctx, contact, o, messageAt)
		pairOK(t, err)
		pairEqual(t, a.Stored, true)
	}
	o.Seq, o.Intent, o.State = 4, 2, "published"
	a, err := db.PeerMessages().AcceptOutcome(ctx, contact, o, messageAt)
	pairOK(t, err)
	if a.Stored {
		t.Fatal("cancelled request accepted a new write intent after uncertainty")
	}
}

func TestReviewOutcomeAcceptanceUnacknowledgedNoticesApplyBackpressure(t *testing.T) {
	f := workspaceStore(t)
	ctx := context.Background()
	for i := 0; i <= store.NoticesPerAgent; i++ {
		id := fmt.Sprintf("finished-%d", i)
		w := f.request(id)
		w.Msg.Seq = uint64(i + 1)
		w.Review = remoteAsk(id).Review
		w.Review.Publish, w.Review.Formal = false, false
		_, err := f.db.QueueWorkspaceSend(ctx, w)
		pairOK(t, err)
		o := messaging.ReviewOutcome{AskID: id, Reviewer: "target", Seq: 1, State: "completed", Attempt: 1, Verdict: "comment", Summary: "done"}
		a, err := f.db.PeerMessages().AcceptOutcome(ctx, f.key, o, messageAt)
		pairOK(t, err)
		if i == store.NoticesPerAgent {
			if a.Stored {
				t.Fatal("unacknowledged notice budget exceeded")
			}
			pairEqual(t, a.Transient, true)
			held, _, err := f.db.PeerMessages().AskOutFor(ctx, f.key, id)
			pairOK(t, err)
			pairEqual(t, held.Seq, uint64(0))
			return
		}
		pairEqual(t, a.Stored, true)
		pending, err := f.db.PendingImports(ctx, 100)
		pairOK(t, err)
		pairEqual(t, len(pending), 1)
		_, err = f.db.ImportRemote(ctx, pending[0], uint64(i+100), messageAt)
		pairOK(t, err)
	}
}

func TestReviewOutcomeAcceptanceAnchorSurvivesMessageRetention(t *testing.T) {
	f := workspaceStore(t)
	ctx := context.Background()
	o := queuedAgentAsk(t, f)
	_, err := f.raw.Exec("DELETE FROM peer_messages WHERE contact = ? AND direction = 'out'", f.key)
	pairOK(t, err)
	o.State, o.Attempt, o.Verdict = "completed", 1, "comment"
	a, err := f.db.PeerMessages().AcceptOutcome(ctx, f.key, o, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	pending, err := f.db.PendingImports(ctx, 10)
	pairOK(t, err)
	pairEqual(t, len(pending), 1)
	got, err := f.db.ImportRemote(ctx, pending[0], 2, messageAt)
	pairOK(t, err)
	pairEqual(t, got.Msg.InReplyTo, "ask")
	if got.Msg.ReviewOutcome == nil {
		t.Fatal("missing structured outcome")
	}
	pairEqual(t, got.Msg.ReviewOutcome.State, "completed")
	pairOK(t, f.db.PeerMessages().Collect(ctx, messageAt.Add(store.AskRetention+time.Hour)))
	_, found, err := f.db.PeerMessages().AskOutFor(ctx, f.key, o.AskID)
	pairOK(t, err)
	pairEqual(t, found, true)
}
