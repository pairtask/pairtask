package store_test

import (
	"context"
	"errors"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/review"
	"testing"
)

func TestConsentPublicationScopeAcceptanceReviewGrantDoesNotSilentlyAuthorizePublish(t *testing.T) {
	for _, reset := range []bool{false, true} {
		name := "fresh-opt-in"
		if reset {
			name = "explicit-publish-reset"
		}
		t.Run(name, func(t *testing.T) {
			f := consentReviewOnlyFixture(t, reset)
			m := consentPublishAsk("needs-publish", true)
			consentHeld(t, f, m, messageAt)
			pending, err := f.db.PendingImports(context.Background(), 20)
			pairOK(t, err)
			pairEqual(t, len(pending), 0)
		})
	}
}
func TestConsentPublicationScopeAcceptanceRequestEvidenceOverridesLowerGrant(t *testing.T) {
	for _, kind := range []string{protocol.ConsentOnce, protocol.ConsentSession} {
		t.Run(kind, func(t *testing.T) {
			c := newConsentPublishCase(t, kind, true)
			c.decide(t)
			g := c.grant(t)
			pairEqual(t, g.Mode, "auto")
			pairEqual(t, g.Publish, false)
			pairEqual(t, g.Formal, false)
			c.dispatch(t)
			c.saveVerdict(t)
			pairOK(t, c.reserve())
			g = c.grant(t)
			pairEqual(t, g.Publish, false)
			pairEqual(t, g.Formal, false)
		})
	}
}
func TestConsentPublicationScopeAcceptanceAlwaysRaisesOnlyShownCeiling(t *testing.T) {
	for _, formal := range []bool{false, true} {
		name := "comments"
		if formal {
			name = "formal"
		}
		t.Run(name, func(t *testing.T) {
			c := newConsentPublishCase(t, protocol.ConsentAlways, formal)
			c.decide(t)
			g := c.grant(t)
			pairEqual(t, g.Mode, "auto")
			pairEqual(t, g.Publish, true)
			pairEqual(t, g.Formal, formal)
			c.dispatch(t)
			c.saveVerdict(t)
			pairOK(t, c.reserve())
		})
	}
}
func TestConsentPublicationScopeAcceptanceExplicitPublishDenyPreservesReviewOnlyGrant(t *testing.T) {
	c := newConsentPublishCase(t, protocol.ConsentOnce, true)
	_, err := c.f.db.DecideConsent(context.Background(), consentDecision(c.view, protocol.ConsentDecline, messageAt))
	pairOK(t, err)
	repeated := c.m
	repeated.ID = "new-publish-id"
	repeated.N++
	a, err := c.f.consentAccept(t, 8, repeated, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Held, false)
	pairEqual(t, a.Stored, false)
	lower := c.m
	lower.ID = "review-only-remains"
	lower.N += 2
	lower.Review = &messaging.ReviewAsk{Forge: "github", Repository: "pairtask/pairtask", Change: "235", Base: c.m.Review.Base, Head: c.m.Review.Head}
	a, err = c.f.consentAccept(t, 8, lower, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	pairEqual(t, a.Held, false)
	g := c.grant(t)
	pairEqual(t, g.Publish, false)
	pairEqual(t, g.Formal, false)
}
func TestConsentPublicationScopeAcceptanceLaterDenyBlocksPublicationReservation(t *testing.T) {
	c := newConsentPublishCase(t, protocol.ConsentOnce, true)
	c.decide(t)
	c.dispatch(t)
	c.saveVerdict(t)
	later := c.m
	later.ID = "later-publish-declined"
	later.N++
	view := consentHeld(t, c.f, later, messageAt)
	_, err := c.f.db.DecideConsent(context.Background(), consentDecision(view, protocol.ConsentDecline, messageAt))
	pairOK(t, err)
	before := consentSnapshot(t, c.f)
	err = c.reserve()
	pairEqual(t, errors.Is(err, review.ErrDenied), true)
	consentUnchanged(t, c.f, before)
}
