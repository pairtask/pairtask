package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func refusedReviewAnswer(t *testing.T, db *store.Store, contact string, m messaging.Message) {
	t.Helper()
	a, err := db.PeerMessages().Accept(context.Background(), contact, m, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, false)
	pairEqual(t, a.Refusal, messaging.Refuse(messaging.ReasonNotShared))
}

func noRefusedReviewDispatch(t *testing.T, db *store.Store, contact, id string) {
	t.Helper()
	ctx := context.Background()
	imports, err := db.PendingImports(ctx, 10)
	pairOK(t, err)
	pairEqual(t, len(imports), 0)
	_, found, err := db.PeerMessages().AskStatus(ctx, contact, id)
	pairOK(t, err)
	pairEqual(t, found, false)
	_, err = db.DecideRemoteReview(ctx, store.RemoteDecision{Contact: contact, RemoteID: id, Approve: true, Now: messageAt})
	pairEqual(t, errors.Is(err, store.ErrNoImport), true)
	reviews, _, err := db.Reviews(ctx, "reviewer", 10)
	pairOK(t, err)
	pairEqual(t, len(reviews), 0)
	notices, err := db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "reviewer", SessionID: "review-session"}, 14)
	pairOK(t, err)
	pairEqual(t, len(notices), 0)
}

func TestRefusalVisibilityReplayAcceptanceRecordsWithoutGrantOrDispatch(t *testing.T) {
	db, contact := messageFixture(t)
	routingRegister(t, db, "reviewer")
	m := remoteAsk("refused-review")
	refusedReviewAnswer(t, db, contact, m)
	rows := messagePage(t, db.PeerMessages(), contact)
	pairEqual(t, len(rows), 1)
	row := rows[0]
	pairEqual(t, row.ID, m.ID)
	pairEqual(t, row.N, m.N)
	pairEqual(t, row.Direction, messaging.DirIn)
	pairEqual(t, row.State, messaging.StateRefused)
	pairEqual(t, row.Refusal, messaging.Refuse(messaging.ReasonNotShared))
	pairEqual(t, row.Text, "")
	pairEqual(t, row.From, m.From)
	pairEqual(t, len(row.To), 1)
	pairEqual(t, row.To[0], m.To[0])
	if row.Review == nil || *row.Review != *m.Review {
		t.Fatalf("refusal lost pinned request: %+v", row.Review)
	}
	grants, err := db.PeerMessages().ReviewGrants(context.Background(), contact)
	pairOK(t, err)
	pairEqual(t, len(grants), 0)
	noRefusedReviewDispatch(t, db, contact, m.ID)
}

func TestRefusalVisibilityReplayAcceptanceGrantAndRestartCannotRevive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refusals.db")
	db := pairSQLite(t, path)
	contact := messageContact(t, db, 1)
	routingRegister(t, db, "reviewer")
	m := remoteAsk("refused-review")
	refusedReviewAnswer(t, db, contact, m)
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	reviewGrant(t, db, contact, store.ReviewAuto)
	refusedReviewAnswer(t, db, contact, m)
	noRefusedReviewDispatch(t, db, contact, m.ID)
	pairEqual(t, len(messagePage(t, db.PeerMessages(), contact)), 1)
	// A new ID and sequence are an explicit new request under the new grant.
	next := remoteAsk("new-review")
	next.N = 2
	a, err := db.PeerMessages().Accept(context.Background(), contact, next, messageAt.Add(time.Minute))
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	imports, err := db.PendingImports(context.Background(), 10)
	pairOK(t, err)
	pairEqual(t, len(imports), 1)
	pairEqual(t, imports[0].RemoteID, next.ID)
}

func TestRefusalVisibilityReplayAcceptanceBindsOmittedTextPinsAndSequence(t *testing.T) {
	for name, change := range map[string]func(*messaging.Message){
		"omitted text":              func(m *messaging.Message) { m.Text = "replacement" },
		"head":                      func(m *messaging.Message) { m.Review.Head = "replacement" },
		"sequence":                  func(m *messaging.Message) { m.N++ },
		"other ID at same sequence": func(m *messaging.Message) { m.ID = "replacement" },
	} {
		t.Run(name, func(t *testing.T) {
			db, contact := messageFixture(t)
			m := remoteAsk("refused-review")
			refusedReviewAnswer(t, db, contact, m)
			reviewGrant(t, db, contact, store.ReviewAuto)
			change(&m)
			a, err := db.PeerMessages().Accept(context.Background(), contact, m, messageAt)
			pairOK(t, err)
			pairEqual(t, a.Stored, false)
			pairEqual(t, a.Refusal, messaging.Refuse(messaging.ReasonConflict))
			pairEqual(t, len(messagePage(t, db.PeerMessages(), contact)), 1)
		})
	}
}

func TestRefusalVisibilityReplayAcceptanceOrdinaryRefusalStaysUnrecorded(t *testing.T) {
	db, contact := messageFixture(t)
	m := remoteAsk("ordinary-message")
	m.Review = nil
	refusedReviewAnswer(t, db, contact, m)
	pairEqual(t, len(messagePage(t, db.PeerMessages(), contact)), 0)
}

func TestRefusalVisibilityReplayAcceptanceFailedRecordDoesNotSettle(t *testing.T) {
	db, raw, contact := messageRawFixture(t)
	_, err := raw.Exec("CREATE TRIGGER fail_refusal BEFORE INSERT ON peer_messages BEGIN SELECT RAISE(ABORT, 'injected refusal failure'); END")
	pairOK(t, err)
	m := remoteAsk("refused-review")
	a, err := db.PeerMessages().Accept(context.Background(), contact, m, messageAt)
	if err == nil || a.Stored || a.Recorded {
		t.Fatalf("failed durable notice returned a successful decision: %+v, %v", a, err)
	}
	pairEqual(t, len(messagePage(t, db.PeerMessages(), contact)), 0)
	_, err = raw.Exec("DROP TRIGGER fail_refusal")
	pairOK(t, err)
	refusedReviewAnswer(t, db, contact, m)
	pairEqual(t, len(messagePage(t, db.PeerMessages(), contact)), 1)
}
