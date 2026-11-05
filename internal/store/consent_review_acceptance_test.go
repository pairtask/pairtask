package store_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
	"testing"
	"time"
)

func consentReviewFixture(t *testing.T) workspaceStoreFixture {
	t.Helper()
	f := consentFixture(t, "reviewer")
	for _, op := range []string{protocol.ConsentReview, protocol.ConsentPublish} {
		pairOK(t, f.db.ResetConsentScope(context.Background(), store.ConsentReset{Contact: f.key, Agent: "reviewer", Operation: op, AuditID: consentAudit(t, f, "consent_reset"), Now: messageAt}))
	}
	return f
}

func TestConsentReviewAcceptanceFullPinsAndPolicyAreImmutable(t *testing.T) {
	for _, field := range []string{"base", "head", "scope", "publish", "formal", "repository"} {
		t.Run(field, func(t *testing.T) {
			f := consentReviewFixture(t)
			m := remoteAsk("frozen-review")
			v := consentHeld(t, f, m, messageAt)
			consentAssertReviewPreview(t, v, m)
			before := consentSnapshot(t, f)
			consentMutateReview(m.Review, field)
			a, err := f.consentAccept(t, 8, m, messageAt.Add(time.Minute))
			conflict := errors.Is(err, store.ErrConsentConflict) || errors.Is(err, messaging.ErrConflict)
			if !conflict && a.Reason != messaging.ReasonConflict {
				t.Fatalf("changed pins or policy replaced pending review: %+v %v", a, err)
			}
			consentUnchanged(t, f, before)
		})
	}
}

func TestConsentReviewAcceptanceExistingAskModeSharesAgentQuota(t *testing.T) {
	f := consentFixture(t, "reviewer")
	reviewGrant(t, f.db, f.key, store.ReviewAsk)
	for i := 0; i < store.ConsentPerAgent-1; i++ {
		m := remoteAsk(fmt.Sprintf("legacy-ask-%d", i))
		m.N = uint64(i + 1)
		a, err := f.consentAccept(t, 7, m, messageAt.Add(time.Duration(i)*time.Minute))
		pairOK(t, err)
		pairEqual(t, a.Stored, true)
	}
	consentHeld(t, f, consentMessage("shared-final-slot", 20, "reviewer"), messageAt.Add(20*time.Minute))
	before := consentSnapshot(t, f)
	a, err := f.consentAccept(t, 8, consentMessage("shared-overflow", 21, "reviewer"), messageAt.Add(21*time.Minute))
	if err == nil && a.Held {
		t.Fatal("new consent ignored 15 existing ask-mode holds")
	}
	consentUnchanged(t, f, before)
}

func consentMutateReview(ask *messaging.ReviewAsk, field string) {
	switch field {
	case "base":
		ask.Base = "replacement"
	case "head":
		ask.Head = "replacement"
	case "scope":
		ask.Scope = "replacement"
	case "publish":
		ask.Publish = false
	case "formal":
		ask.Formal = false
	case "repository":
		ask.Repository = "other/repository"
	}
}

func consentAssertReviewPreview(t *testing.T, v protocol.ConsentView, m messaging.Message) {
	t.Helper()
	pinsMatch := v.Review != nil && v.Review.Base == m.Review.Base && v.Review.Head == m.Review.Head
	policyMatches := v.Review != nil && v.Review.Publish && v.Review.Formal
	if !pinsMatch || !policyMatches {
		t.Fatalf("review preview lost pins or policy ceiling: %+v", v.Review)
	}
}
