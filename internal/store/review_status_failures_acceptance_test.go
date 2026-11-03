package store_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/review"
	"github.com/pairtask/pairtask/internal/store"
)

func statusFailureAsk(t *testing.T, f workspaceStoreFixture, id string) {
	t.Helper()
	m := remoteAsk(id)
	_, err := f.db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: f.key, ID: id, Text: m.Text, To: m.To, Review: m.Review, Now: messageAt})
	pairOK(t, err)
}

func statusFailureCounts(t *testing.T, db *store.Store) protocol.ReviewCounts {
	t.Helper()
	c, err := db.ReviewCounts(context.Background())
	pairOK(t, err)
	if c.AskedActive == nil {
		t.Fatal("current broker omitted authoritative asked_active")
	}
	return c
}

func statusFailurePage(t *testing.T, db *store.Store, before string) protocol.ReviewFailedPage {
	t.Helper()
	p, err := db.FailedReviews(context.Background(), before)
	pairOK(t, err)
	return p
}

func statusFailureReview(t *testing.T, db *store.Store, id, publication string) {
	t.Helper()
	r := review.New(id, "operator", protocol.ReviewSpec{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "298"}, Reviewer: "reviewer"}, messageAt)
	r.View.State, r.View.Attempt = protocol.ReviewCompleted, 1
	r.View.Publication = protocol.Publication{State: publication, Intent: 1, Reason: "publication problem"}
	_, _, err := db.CreateReview(context.Background(), r, store.ReviewAction{Actor: "fixture", Action: "seed"})
	pairOK(t, err)
}

func TestReviewStatusFailuresAcceptanceOpenFailuresAreNotActiveProcesses(t *testing.T) {
	for _, state := range []string{"publication_failed", "uncertain"} {
		t.Run(state, func(t *testing.T) {
			f := workspaceStore(t)
			o := queuedAgentAsk(t, f)
			o.Verdict = "comment"
			o.State, o.Attempt, o.Intent = state, 1, 1
			pairEqual(t, acceptReviewID(t, f, o).Stored, true)
			c := statusFailureCounts(t, f.db)
			pairEqual(t, c.AskedOpen, 1)
			pairEqual(t, *c.AskedActive, 0)
			pairEqual(t, c.Failed, 1)
			p := statusFailurePage(t, f.db, "")
			pairEqual(t, p.Total, 1)
			pairEqual(t, p.Token, c.FailedToken)
			pairEqual(t, p.Items[0].State, state)
		})
	}
}

func TestReviewStatusFailuresAcceptancePagedTiesIncludeKindContactAndID(t *testing.T) {
	db, raw, key := messageRawFixture(t)
	keys := []string{key, messageContact(t, db, 2), messageContact(t, db, 3)}
	var expected []string
	for i := 0; i < 36; i++ {
		f := workspaceStoreFixture{db, raw, keys[i%3]}
		id := fmt.Sprintf("same-%02d", i/3)
		statusFailureAsk(t, f, id)
		_, err := raw.Exec(`UPDATE review_asks_out SET state='refused',settled_at=?,updated_at=? WHERE contact=? AND ask_id=?`, messageAt.Format(time.RFC3339Nano), messageAt.Format(time.RFC3339Nano), f.key, id)
		pairOK(t, err)
		expected = append(expected, "ask\x00"+f.key+"\x00"+id)
	}
	for _, state := range []string{protocol.PublicationFailed, protocol.PublicationUncertain} {
		statusFailureReview(t, db, state, state)
		expected = append(expected, "review\x00\x00"+state)
	}
	sort.Strings(expected)
	c := statusFailureCounts(t, db)
	pairEqual(t, c.Failed, len(expected))
	pairEqual(t, len(c.FailedToken), 32)
	var got []string
	before := ""
	for {
		page := statusFailurePage(t, db, before)
		pairEqual(t, page.Total, len(expected))
		pairEqual(t, page.Token, c.FailedToken)
		for _, item := range page.Items {
			got = append(got, item.Kind+"\x00"+item.Contact+"\x00"+item.ID)
		}
		if !page.More {
			pairEqual(t, page.Next, "")
			break
		}
		pairEqual(t, len(page.Items), protocol.ReviewPendingPage)
		if page.Next == "" || page.Next == before {
			t.Fatal("failed cursor did not advance")
		}
		before = page.Next
	}
	pairEqual(t, fmt.Sprint(got), fmt.Sprint(expected))
}

func TestReviewStatusFailuresAcceptanceSameAskRefailureChangesToken(t *testing.T) {
	f := workspaceStore(t)
	o := queuedAgentAsk(t, f)
	o.Verdict = "comment"
	o.State, o.Attempt, o.Intent = "publication_failed", 1, 1
	pairEqual(t, acceptReviewID(t, f, o).Stored, true)
	first := statusFailureCounts(t, f.db)
	// Retrying a write leaves the same ask open, then fails under a new intent.
	o.Seq, o.Intent, o.State = 2, 2, "publishing"
	pairEqual(t, acceptReviewID(t, f, o).Stored, true)
	active := statusFailureCounts(t, f.db)
	pairEqual(t, active.Failed, 0)
	pairEqual(t, *active.AskedActive, 1)
	o.Seq, o.State = 3, "publication_failed"
	pairEqual(t, acceptReviewID(t, f, o).Stored, true)
	again := statusFailureCounts(t, f.db)
	pairEqual(t, again.Failed, 1)
	pairEqual(t, again.FailedToken != first.FailedToken, true)
	pairEqual(t, statusFailurePage(t, f.db, "").Token, again.FailedToken)
}

func TestReviewStatusFailuresAcceptanceUnresolvedPublicationFailureNeverAgesOut(t *testing.T) {
	db, raw, _ := messageRawFixture(t)
	statusFailureReview(t, db, "old-unresolved", protocol.PublicationFailed)
	_, err := raw.Exec(`UPDATE reviews SET updated_at=? WHERE review_id='old-unresolved'`, messageAt.Add(-365*24*time.Hour).Format(time.RFC3339Nano))
	pairOK(t, err)
	c := statusFailureCounts(t, db)
	pairEqual(t, c.Failed, 1)
	pairEqual(t, statusFailurePage(t, db, "").Items[0].ID, "old-unresolved")
}

func TestReviewStatusFailuresAcceptanceInvalidCursorAndClosedStoreAreErrors(t *testing.T) {
	db, _ := messageFixture(t)
	if _, err := db.FailedReviews(context.Background(), "not-a-cursor"); err == nil {
		t.Fatal("malformed failure cursor reported an empty page")
	}
	pairOK(t, db.Close())
	if _, err := db.FailedReviews(context.Background(), ""); err == nil {
		t.Fatal("closed store reported authoritative empty failures")
	}
}

func TestReviewStatusFailuresAcceptanceLocalRetryGenerationsChangeFailureToken(t *testing.T) {
	db, _ := messageFixture(t)
	statusFailureReview(t, db, "same-review", protocol.PublicationFailed)
	before := statusFailureCounts(t, db)
	for _, field := range []string{"attempt", "intent", "state"} {
		_, _, err := db.ChangeReview(context.Background(), "same-review", store.ReviewAction{Actor: "fixture", Action: field}, func(r review.Record) (review.Record, bool, error) {
			switch field {
			case "attempt":
				r.View.Attempt++
			case "intent":
				r.View.Publication.Intent++
			case "state":
				r.View.Publication.State = protocol.PublicationUncertain
			}
			return r, true, nil
		})
		pairOK(t, err)
		after := statusFailureCounts(t, db)
		pairEqual(t, after.Failed, 1)
		if after.FailedToken == before.FailedToken {
			t.Fatalf("failure token ignored changed %s", field)
		}
		before = after
	}
}

func TestReviewStatusFailuresAcceptanceFailureClassificationMatchesRetainedStates(t *testing.T) {
	for _, tc := range []struct {
		state   string
		settled bool
		failed  int
	}{
		{"publication_failed", true, 1}, {"uncertain", true, 1},
		{"refused", true, 1}, {"unsent", true, 1}, {"expired", true, 1},
		{"review_expired", true, 1}, {"contact_removed", true, 1},
		{"cancelled", true, 0}, {"declined", true, 0}, {"withheld", true, 0},
		{"stale", true, 0}, {"published", true, 0}, {"completed", true, 0},
	} {
		t.Run(tc.state, func(t *testing.T) {
			f := workspaceStore(t)
			statusFailureAsk(t, f, "classification")
			_, err := f.raw.Exec(`UPDATE review_asks_out SET state=?,settled_at=CASE WHEN ? THEN updated_at ELSE NULL END WHERE contact=? AND ask_id='classification'`, tc.state, tc.settled, f.key)
			pairOK(t, err)
			c := statusFailureCounts(t, f.db)
			pairEqual(t, c.Failed, tc.failed)
			pairEqual(t, *c.AskedActive, 0)
			p := statusFailurePage(t, f.db, "")
			pairEqual(t, p.Total, tc.failed)
			pairEqual(t, len(p.Items), tc.failed)
			pairEqual(t, p.Token, c.FailedToken)
		})
	}
}

func TestReviewStatusFailuresAcceptanceTimestampOnlyReorderChangesSetToken(t *testing.T) {
	db, raw, key := messageRawFixture(t)
	for i := 0; i < 33; i++ {
		f := workspaceStoreFixture{db, raw, key}
		id := fmt.Sprintf("ordered-%02d", i)
		statusFailureAsk(t, f, id)
		_, err := raw.Exec(`UPDATE review_asks_out SET state='refused',settled_at=updated_at WHERE contact=? AND ask_id=?`, key, id)
		pairOK(t, err)
	}
	first := statusFailurePage(t, db, "")
	pairEqual(t, first.More, true)
	pairEqual(t, len(first.Items), 32)
	// The last row moves ahead of the first page without changing any of
	// its lifecycle fields. An old cursor must no longer have the same token.
	_, err := raw.Exec(`UPDATE review_asks_out SET updated_at=? WHERE contact=? AND ask_id='ordered-32'`, messageAt.Add(time.Hour).Format(time.RFC3339Nano), key)
	pairOK(t, err)
	later := statusFailurePage(t, db, first.Next)
	if later.Token == first.Token {
		t.Fatal("timestamp-only page reorder left failure token unchanged")
	}
	restart := statusFailurePage(t, db, "")
	pairEqual(t, restart.Items[0].ID, "ordered-32")
	pairEqual(t, restart.Token, later.Token)
}
