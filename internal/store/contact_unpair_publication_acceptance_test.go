package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/review"
	"github.com/pairtask/pairtask/internal/store"
)

func unpairPublicationContact(t *testing.T, c *consentPublishCase) {
	t.Helper()
	f := &operatorContactFixture{db: c.f.db, raw: c.f.raw, contact: c.f.key}
	_, _, err := f.db.UnpairContact(context.Background(), unpairRequest(t, f, "publication-unpair"))
	pairOK(t, err)
}

func TestContactUnpairPublicationAcceptanceNoNewWriteFromRetainedAuthority(t *testing.T) {
	for _, kind := range []string{protocol.ConsentAlways, protocol.ConsentOnce, protocol.ConsentSession} {
		t.Run(kind, func(t *testing.T) {
			c := newConsentPublishCase(t, kind, true)
			c.decide(t)
			c.dispatch(t)
			unpairPublicationContact(t, c)
			// Already-dispatched work still completes and saves its result here.
			c.saveVerdict(t)
			before, found, err := c.f.db.GetReview(context.Background(), c.reviewID)
			pairOK(t, err)
			pairEqual(t, found, true)
			events := epochRows(t, c.f.raw, "review_events", epochColumns(t, c.f.raw, "review_events"))
			err = c.reserve()
			pairEqual(t, errors.Is(err, review.ErrDenied), true)
			after, _, err := c.f.db.GetReview(context.Background(), c.reviewID)
			pairOK(t, err)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("refused publication changed review or write intent")
			}
			unpairAssertEvidence(t, &operatorContactFixture{raw: c.f.raw}, "review_events", events)
		})
	}
}

func TestContactUnpairPublicationAcceptanceExistingIntentCanRecordItsResult(t *testing.T) {
	c := newConsentPublishCase(t, protocol.ConsentAlways, true)
	c.decide(t)
	c.dispatch(t)
	c.saveVerdict(t)
	pairOK(t, c.reserve())
	unpairPublicationContact(t, c)
	updated, changed, err := c.f.db.ChangeReview(context.Background(), c.reviewID,
		store.ReviewAction{Actor: "reviewer", Action: protocol.TypeReviewPublication},
		func(r review.Record) (review.Record, bool, error) {
			next, step, err := review.Publish(r, r.View.Attempt, protocol.PublicationUpdate{
				From: protocol.PublicationSubmitting, To: protocol.PublicationFailed,
				Intent: r.View.Publication.Intent, Reason: "unavailable"}, messageAt)
			return next, step.Changed, err
		})
	pairOK(t, err)
	pairEqual(t, changed, true)
	pairEqual(t, updated.View.Publication.State, protocol.PublicationFailed)
	pairEqual(t, updated.View.Publication.Intent, 1)
}
