package store_test

import (
	"context"
	"fmt"
	"github.com/pairtask/pairtask/internal/store"
	"testing"
	"time"
)

func TestConsentLimitsAcceptanceDuplicateAtRateLimitStillReplays(t *testing.T) {
	f := consentFixture(t, "alpha")
	var firstID string
	for i := 0; i < store.ConsentBurst; i++ {
		v := consentHeld(t, f, consentMessage(fmt.Sprintf("burst-%d", i), uint64(i+1), "alpha"), messageAt)
		if i == 0 {
			firstID = v.ID
		}
	}
	before := consentSnapshot(t, f)
	a, err := f.consentAccept(t, 8, consentMessage("burst-0", 1, "alpha"), messageAt)
	pairOK(t, err)
	pairEqual(t, a.HeldID, firstID)
	consentUnchanged(t, f, before)
	a, err = f.consentAccept(t, 8, consentMessage("over-burst", 11, "alpha"), messageAt)
	accepted := a.Held || a.Stored
	if err == nil && accepted {
		t.Fatalf("rate bound admitted extra request: %+v", a)
	}
	consentUnchanged(t, f, before)
	consentHeld(t, f, consentMessage("refilled", 12, "alpha"), messageAt.Add(time.Minute))
}

func TestConsentLimitsAcceptanceConcurrentLastAgentSlotIsAtomic(t *testing.T) {
	f := consentFixture(t, "alpha", "beta")
	for i := 0; i < store.ConsentPerAgent-1; i++ {
		consentHeld(t, f, consentMessage(fmt.Sprintf("fill-%d", i), uint64(i+1), "alpha"), messageAt.Add(time.Duration(i)*time.Minute))
	}
	now := messageAt.Add(20 * time.Minute)
	start := make(chan struct{})
	results := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			<-start
			a, err := f.consentAccept(t, 8, consentMessage(fmt.Sprintf("last-%d", i), uint64(30+i), "alpha", "beta"), now)
			results <- err == nil && a.Held
		}(i)
	}
	close(start)
	admitted := 0
	for i := 0; i < 2; i++ {
		if <-results {
			admitted++
		}
	}
	pairEqual(t, admitted, 1)
	page, err := f.db.ConsentPage(context.Background(), f.key, 0, 100)
	pairOK(t, err)
	pairEqual(t, page.Total, store.ConsentPerAgent)
	// Failed multi-target admission must not reserve beta's remaining capacity.
	for i := 0; i < store.ConsentPerAgent-1; i++ {
		consentHeld(t, f, consentMessage(fmt.Sprintf("beta-%d", i), uint64(40+i), "beta"), now.Add(time.Duration(i+1)*time.Minute))
	}
}
