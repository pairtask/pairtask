package store_test

import (
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/store"
)

func TestPairStatusSummaryAcceptanceCountsOnlyAwaitingIncomingReviews(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	routingRegister(t, db, "reviewer")
	reviewGrant(t, db, contact, store.ReviewAsk)
	for i, id := range []string{"approve", "decline"} {
		m := remoteAsk(id)
		m.N = uint64(i + 1)
		_, err := db.PeerMessages().Accept(ctx, contact, m, messageAt)
		pairOK(t, err)
	}
	counts, err := db.PeerMessages().AwaitingReviewCounts(ctx)
	pairOK(t, err)
	pairEqual(t, counts[contact], 2)
	for _, decision := range []struct {
		id        string
		approve   bool
		remaining int
	}{
		{"approve", true, 1}, {"decline", false, 0},
	} {
		_, err = db.DecideRemoteReview(ctx, store.RemoteDecision{Contact: contact, RemoteID: decision.id, Approve: decision.approve, Now: messageAt})
		pairOK(t, err)
		counts, err = db.PeerMessages().AwaitingReviewCounts(ctx)
		pairOK(t, err)
		pairEqual(t, counts[contact], decision.remaining)
	}
	reviewGrant(t, db, contact, store.ReviewAuto)
	auto := remoteAsk("auto")
	auto.N = 3
	_, err = db.PeerMessages().Accept(ctx, contact, auto, messageAt)
	pairOK(t, err)
	counts, err = db.PeerMessages().AwaitingReviewCounts(ctx)
	pairOK(t, err)
	pairEqual(t, counts[contact], 0)
}

func TestPairStatusSummaryAcceptanceCountsStayScopedToContact(t *testing.T) {
	db, first := messageFixture(t)
	second := messageContact(t, db, 2)
	ctx := context.Background()
	routingRegister(t, db, "reviewer")
	for i, contact := range []string{first, second} {
		reviewGrant(t, db, contact, store.ReviewAsk)
		for n := 1; n <= i+1; n++ {
			m := remoteAsk(string(rune('a' + n)))
			m.N = uint64(n)
			_, err := db.PeerMessages().Accept(ctx, contact, m, messageAt)
			pairOK(t, err)
		}
	}
	counts, err := db.PeerMessages().AwaitingReviewCounts(ctx)
	pairOK(t, err)
	pairEqual(t, counts[first], 1)
	pairEqual(t, counts[second], 2)
}
