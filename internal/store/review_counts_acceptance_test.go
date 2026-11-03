package store_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func reviewCountsAwaiting(t *testing.T, db *store.Store, key string, n int) {
	t.Helper()
	_, err := db.PeerMessages().SetReviewGrant(context.Background(), key, store.ReviewGrantChange{Agents: []string{"reviewer-0", "reviewer-1", "reviewer-2"}, Mode: store.ReviewAsk, Now: messageAt})
	pairOK(t, err)
	for i := 1; i <= n; i++ {
		m := remoteAsk(fmt.Sprintf("pending-%02d", i))
		m.N = uint64(i)
		m.To = []string{fmt.Sprint("reviewer-", i%3)}
		a, err := db.PeerMessages().Accept(context.Background(), key, m, messageAt)
		pairOK(t, err)
		pairEqual(t, a.Stored, true)
	}
}

func TestReviewCountsStoreAcceptanceAwaitingPagesDoNotSkipTiedItems(t *testing.T) {
	db, key := messageFixture(t)
	reviewCountsAwaiting(t, db, key, 36)
	ctx := context.Background()
	counts, err := db.ReviewCounts(ctx)
	pairOK(t, err)
	pairEqual(t, counts.AwaitingYou, 36)
	seen := map[string]bool{}
	before := ""
	for {
		page, err := db.AwaitingReviews(ctx, before)
		pairOK(t, err)
		pairEqual(t, page.Total, 36)
		pairEqual(t, page.Token, counts.AwaitingToken)
		for _, item := range page.Items {
			if seen[item.MessageID] {
				t.Fatalf("awaiting item repeated: %s", item.MessageID)
			}
			seen[item.MessageID] = true
			pairEqual(t, item.Contact, key)
			pairEqual(t, item.Cursor > 0, true)
			pairEqual(t, item.Ask != nil, true)
		}
		if !page.More {
			pairEqual(t, page.Next, "")
			break
		}
		pairEqual(t, len(page.Items), 32)
		if page.Next == "" || page.Next == before {
			t.Fatal("awaiting cursor failed to advance")
		}
		before = page.Next
	}
	pairEqual(t, len(seen), 36)
}

type countedAsk struct {
	contact, id string
	settled     bool
	at          time.Time
}

func reviewCountsAsked(t *testing.T, db *store.Store, keys []string) []countedAsk {
	t.Helper()
	var expected []countedAsk
	for i := 0; i < 36; i++ {
		key, id := keys[i%len(keys)], fmt.Sprintf("ask-%02d", i)
		m := remoteAsk(id)
		_, err := db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: key, ID: id, Text: m.Text, To: m.To, Review: m.Review, Now: messageAt})
		pairOK(t, err)
		item := countedAsk{contact: key, id: id, at: messageAt, settled: i%2 == 1}
		if item.settled {
			item.at = messageAt.Add(time.Hour)
			a, err := db.PeerMessages().AcceptOutcome(context.Background(), key, messaging.ReviewOutcome{AskID: id, Reviewer: "reviewer", Seq: 1, State: "declined", Reason: "unavailable"}, item.at)
			pairOK(t, err)
			pairEqual(t, a.Stored, true)
		}
		expected = append(expected, item)
	}
	sort.Slice(expected, func(i, j int) bool {
		a, b := expected[i], expected[j]
		if a.settled != b.settled {
			return !a.settled
		}
		if !a.at.Equal(b.at) {
			return a.at.After(b.at)
		}
		if a.contact != b.contact {
			return a.contact < b.contact
		}
		return a.id < b.id
	})
	return expected
}

func TestReviewCountsStoreAcceptanceAskedOrderAndCursorCoverSettledBoundary(t *testing.T) {
	db, key := messageFixture(t)
	expected := reviewCountsAsked(t, db, []string{key, messageContact(t, db, 2), messageContact(t, db, 3)})
	ctx := context.Background()
	var got []protocol.ReviewAskedItem
	before := ""
	for {
		page, err := db.AskedReviews(ctx, before)
		pairOK(t, err)
		pairEqual(t, page.Total, 36)
		got = append(got, page.Items...)
		if !page.More {
			pairEqual(t, page.Next, "")
			break
		}
		if page.Next == "" || page.Next == before {
			t.Fatal("asked cursor failed to advance")
		}
		before = page.Next
	}
	pairEqual(t, len(got), len(expected))
	for i, item := range got {
		pairEqual(t, item.Contact, expected[i].contact)
		pairEqual(t, item.AskID, expected[i].id)
		pairEqual(t, item.Settled, expected[i].settled)
		pairEqual(t, item.Cursor > 0, true)
	}
	counts, err := db.ReviewCounts(ctx)
	pairOK(t, err)
	pairEqual(t, counts.AskedOpen, 18)
}

func TestReviewCountsStoreAcceptanceEmptyAndFailedQueriesAreDistinct(t *testing.T) {
	db, _ := messageFixture(t)
	counts, err := db.ReviewCounts(context.Background())
	pairOK(t, err)
	if counts.AskedActive == nil {
		t.Fatal("empty current counts omitted asked_active")
	}
	pairEqual(t, *counts.AskedActive, 0)
	counts.AskedActive = nil
	pairEqual(t, counts, protocol.ReviewCounts{})
	pairOK(t, db.Close())
	_, err = db.ReviewCounts(context.Background())
	if err == nil {
		t.Fatal("failed count query reported an authoritative empty result")
	}
}

func TestReviewCountsStoreAcceptanceAskedIdentityOutlivesPrunedMessage(t *testing.T) {
	db, raw, key := messageRawFixture(t)
	o := outcomeAsk(t, db, key)
	_, err := raw.Exec(`DELETE FROM peer_messages WHERE contact=? AND direction='out' AND message_id=?`, key, o.AskID)
	pairOK(t, err)
	page, err := db.AskedReviews(context.Background(), "")
	pairOK(t, err)
	pairEqual(t, page.Total, 1)
	pairEqual(t, len(page.Items), 1)
	pairEqual(t, page.Items[0].AskID, o.AskID)
	pairEqual(t, page.Items[0].Contact, key)
	pairEqual(t, page.Items[0].Cursor, int64(0))
	counts, err := db.ReviewCounts(context.Background())
	pairOK(t, err)
	pairEqual(t, counts.AskedOpen, 1)
}

func TestReviewCountsStoreAcceptanceTokenTracksSetRatherThanArrivalClock(t *testing.T) {
	db, raw, key := messageRawFixture(t)
	reviewCountsAwaiting(t, db, key, 2)
	first, err := db.ReviewCounts(context.Background())
	pairOK(t, err)
	_, err = raw.Exec(`UPDATE remote_imports SET staged_at=? WHERE contact=?`, messageAt.Add(-time.Hour).Format(time.RFC3339Nano), key)
	pairOK(t, err)
	after, err := db.ReviewCounts(context.Background())
	pairOK(t, err)
	pairEqual(t, after.AwaitingToken, first.AwaitingToken)
	_, err = db.DecideRemoteReview(context.Background(), store.RemoteDecision{Contact: key, RemoteID: "pending-01", Approve: false, Now: messageAt})
	pairOK(t, err)
	after, err = db.ReviewCounts(context.Background())
	pairOK(t, err)
	pairEqual(t, after.AwaitingYou, 1)
	pairEqual(t, after.AwaitingToken != first.AwaitingToken, true)
}
