package store_test

import (
	"context"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/review"
	"github.com/pairtask/pairtask/internal/store"
	"strings"
	"testing"
)

type consentPublishCase struct {
	f        workspaceStoreFixture
	m        messaging.Message
	view     protocol.ConsentView
	kind     string
	reviewID string
}

func consentReviewOnlyFixture(t *testing.T, reset bool) workspaceStoreFixture {
	t.Helper()
	f := consentFixture(t, "reviewer")
	_, err := f.db.PeerMessages().SetReviewGrant(context.Background(), f.key, store.ReviewGrantChange{Agents: []string{"reviewer"}, Mode: store.ReviewAuto, Now: messageAt})
	pairOK(t, err)
	if reset {
		pairOK(t, f.db.ResetConsentScope(context.Background(), store.ConsentReset{Contact: f.key, Agent: "reviewer", Operation: protocol.ConsentPublish, AuditID: 0, Now: messageAt}))
	}
	return f
}
func consentPublishAsk(id string, formal bool) messaging.Message {
	m := remoteAsk(id)
	m.Review.Base = strings.Repeat("a", 40)
	m.Review.Head = strings.Repeat("b", 40)
	m.Review.Formal = formal
	return m
}
func newConsentPublishCase(t *testing.T, kind string, formal bool) *consentPublishCase {
	t.Helper()
	f := consentReviewOnlyFixture(t, true)
	m := consentPublishAsk("publish-only-undecided", formal)
	return &consentPublishCase{f: f, m: m, view: consentHeld(t, f, m, messageAt), kind: kind}
}
func (c *consentPublishCase) decide(t *testing.T) {
	t.Helper()
	if c.kind == protocol.ConsentSession {
		pairOK(t, consentRequireOwner(t, c.f).SetConsentOwner(context.Background(), "broker-epoch", 7))
	}
	_, err := c.f.db.DecideConsent(context.Background(), consentDecision(c.view, c.kind, messageAt))
	pairOK(t, err)
}
func (c *consentPublishCase) grant(t *testing.T) store.ReviewGrant {
	t.Helper()
	grants, err := c.f.db.PeerMessages().ReviewGrants(context.Background(), c.f.key)
	pairOK(t, err)
	for _, g := range grants {
		if g.Agent == "reviewer" {
			return g
		}
	}
	t.Fatal("lower review grant disappeared")
	return store.ReviewGrant{}
}
func (c *consentPublishCase) dispatch(t *testing.T) {
	t.Helper()
	imp, err := c.f.db.ImportRemote(context.Background(), store.RemoteImport{Contact: c.f.key, RemoteID: c.m.ID}, 1, messageAt)
	pairOK(t, err)
	pairEqual(t, imp.State, store.ImportImported)
	c.reviewID = imp.ReviewID
	rec, found, err := c.f.db.GetReview(context.Background(), c.reviewID)
	pairOK(t, err)
	pairEqual(t, found, true)
	if rec.View.Spec.Policy == nil {
		t.Fatal("approved review lost publication ceiling")
	}
	pairEqual(t, rec.View.Spec.Policy.Publish, true)
	pairEqual(t, rec.View.Spec.Policy.Formal, c.m.Review.Formal)
}
func (c *consentPublishCase) step(t *testing.T, fn func(review.Record) (review.Record, bool, error)) {
	t.Helper()
	_, _, err := c.f.db.ChangeReview(context.Background(), c.reviewID, store.ReviewAction{Actor: "reviewer", Action: "acceptance fixture"}, fn)
	pairOK(t, err)
}
func (c *consentPublishCase) saveVerdict(t *testing.T) {
	t.Helper()
	c.step(t, func(r review.Record) (review.Record, bool, error) {
		return review.Prepare(r, protocol.Revisions{Base: c.m.Review.Base, Head: c.m.Review.Head}, messageAt)
	})
	c.step(t, func(r review.Record) (review.Record, bool, error) { return review.Accept(r, messageAt) })
	c.step(t, func(r review.Record) (review.Record, bool, error) {
		return review.Submit(r, r.View.Attempt, protocol.Verdict{Outcome: protocol.OutcomeApprove, Summary: "exact approved request"}, messageAt)
	})
}
func (c *consentPublishCase) reserve() error {
	_, _, err := c.f.db.ChangeReview(context.Background(), c.reviewID, store.ReviewAction{Actor: "reviewer", Action: protocol.TypeReviewPublication, Owner: store.LiveOwner{Epoch: "broker-epoch", Gen: 7}}, func(r review.Record) (review.Record, bool, error) {
		next, step, err := review.Publish(r, r.View.Attempt, protocol.PublicationUpdate{From: protocol.PublicationPending, To: protocol.PublicationSubmitting, Token: "publication-scope-intent", Identity: "fixture-publisher"}, messageAt)
		return next, step.Changed, err
	})
	return err
}
