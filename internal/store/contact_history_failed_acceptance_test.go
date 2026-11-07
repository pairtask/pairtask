package store_test

import (
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestContactHistoryAcceptanceFailedPagesKeepTiedEpochs(t *testing.T) {
	f := namespaceFixture(t)
	f.sweepQueueAsk(t)
	namespaceRepair(t, f)
	f.sweepCloneAsks(t, protocol.ReviewPendingPage+1)
	var items []protocol.ReviewFailedItem
	before := ""
	for pass := 0; pass < 3; pass++ {
		page, err := f.db.FailedReviews(context.Background(), before)
		pairOK(t, err)
		pairEqual(t, page.Total, protocol.ReviewPendingPage+1)
		items = append(items, page.Items...)
		if !page.More {
			break
		}
		if page.Next == "" {
			t.Fatal("more failures without next cursor")
		}
		before = page.Next
	}
	pairEqual(t, len(items), protocol.ReviewPendingPage+1)
	failedEpochsUnique(t, items)
}

func failedEpochsUnique(t *testing.T, items []protocol.ReviewFailedItem) {
	t.Helper()
	seen := map[int64]bool{}
	for _, item := range items {
		if item.PairingEpoch <= 0 {
			t.Fatal("failure lacks source pairing epoch")
		}
		if seen[item.PairingEpoch] {
			t.Fatalf("repeated failure epoch %d", item.PairingEpoch)
		}
		seen[item.PairingEpoch] = true
	}
}

func TestContactHistoryAcceptanceFailureTokenNamesEpoch(t *testing.T) {
	f := namespaceFixture(t)
	f.sweepQueueAsk(t)
	namespaceRepair(t, f)
	_, err := f.raw.Exec(`UPDATE review_asks_out SET pairing_epoch=2 WHERE pairing_epoch=1`)
	pairOK(t, err)
	first, err := f.db.FailedReviews(context.Background(), "")
	pairOK(t, err)
	pairEqual(t, first.Total, 1)
	_, err = f.raw.Exec(`UPDATE review_asks_out SET pairing_epoch=3 WHERE pairing_epoch=2`)
	pairOK(t, err)
	second, err := f.db.FailedReviews(context.Background(), "")
	pairOK(t, err)
	pairEqual(t, second.Total, 1)
	if first.Token == second.Token {
		t.Fatal("failure token did not change when its pairing identity changed")
	}
}

func TestContactHistoryAcceptanceLegacyFailureTokenIsStable(t *testing.T) {
	f := namespaceFixture(t)
	f.sweepQueueAsk(t)
	namespaceRepair(t, f)
	page, err := f.db.FailedReviews(context.Background(), "")
	pairOK(t, err)
	pairEqual(t, page.Total, 1)
	// Captured from the pre-history implementation, not a second hash implementation.
	pairEqual(t, page.Token, "7804a3b9528c1b0c75f2372906bb5ecc")
}
