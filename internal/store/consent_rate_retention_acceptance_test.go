package store_test

import (
	"context"
	"fmt"
	"github.com/pairtask/pairtask/internal/store"
	"testing"
	"time"
)

func TestConsentRateRetentionAcceptanceFullEffectiveBucketCanBeReclaimed(t *testing.T) {
	f := consentFixture(t, "alpha")
	consentHeld(t, f, consentMessage("one-token", 1, "alpha"), messageAt)
	pairOK(t, f.db.PeerMessages().Collect(context.Background(), messageAt.Add(store.ConsentRefill)))
	var count int
	pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM consent_rate WHERE contact=?`, f.key).Scan(&count))
	pairEqual(t, count, 0)
	consentHeld(t, f, consentMessage("after-safe-reclaim", 2, "alpha"), messageAt.Add(store.ConsentRefill))
}

func TestConsentRateRetentionAcceptancePartialRefillCannotResetBucket(t *testing.T) {
	f := consentFixture(t, "a", "b", "c")
	agents := []string{"a", "b", "c"}
	for i := 0; i < store.ConsentBurst; i++ {
		consentHeld(t, f, consentMessage(fmt.Sprintf("drain-%d", i), uint64(i+1), agents[i%3]), messageAt)
	}
	now := messageAt.Add(time.Duration(store.ConsentBurst-1) * store.ConsentRefill)
	pairOK(t, f.db.PeerMessages().Collect(context.Background(), now))
	f = consentStoreReopen(t, f)
	var count int
	pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM consent_rate WHERE contact=?`, f.key).Scan(&count))
	pairEqual(t, count, 1)
	for i := 0; i < store.ConsentBurst-1; i++ {
		consentHeld(t, f, consentMessage(fmt.Sprintf("refill-%d", i), uint64(20+i), agents[i%3]), now)
	}
	a, err := f.consentAccept(t, 8, consentMessage("no-reset-extra", 40, "a"), now)
	if err == nil && a.Held {
		t.Fatal("compaction or reopen reset partially refilled bucket")
	}
}
