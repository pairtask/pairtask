package store_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestContactUnpairOutcomeAcceptanceLocalCompletionDoesNotReviveRetiredOutcome(t *testing.T) {
	c := newConsentPublishCase(t, protocol.ConsentOnce, true)
	c.decide(t)
	c.dispatch(t)
	unpairPublicationContact(t, c)
	seq := epochRows(t, c.f.raw, "review_outcome_seq", epochColumns(t, c.f.raw, "review_outcome_seq"))
	out := epochRows(t, c.f.raw, "review_outcomes_out", epochColumns(t, c.f.raw, "review_outcomes_out"))
	if len(seq) == 0 || len(out) == 0 {
		t.Fatal("fixture must retain an outcome and its numbering tombstone")
	}
	c.saveVerdict(t)
	rec, found, err := c.f.db.GetReview(context.Background(), c.reviewID)
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, rec.View.State, protocol.ReviewCompleted)
	if !reflect.DeepEqual(seq, epochRows(t, c.f.raw, "review_outcome_seq", epochColumns(t, c.f.raw, "review_outcome_seq"))) {
		t.Error("local completion advanced retired outcome numbering")
	}
	if !reflect.DeepEqual(out, epochRows(t, c.f.raw, "review_outcomes_out", epochColumns(t, c.f.raw, "review_outcomes_out"))) {
		t.Error("local completion replaced an outcome retired by unpair")
	}
}

func TestContactUnpairOutcomeAcceptanceActivePairingStillQueuesCompletion(t *testing.T) {
	c := newConsentPublishCase(t, protocol.ConsentOnce, true)
	c.decide(t)
	c.dispatch(t)
	before := epochRows(t, c.f.raw, "review_outcomes_out", epochColumns(t, c.f.raw, "review_outcomes_out"))
	c.saveVerdict(t)
	after := epochRows(t, c.f.raw, "review_outcomes_out", epochColumns(t, c.f.raw, "review_outcomes_out"))
	if reflect.DeepEqual(before, after) {
		t.Fatal("active pairing did not queue its completed review outcome")
	}
	pending, err := c.f.db.PeerMessages().PendingOutcomes(context.Background(), c.f.key, 10)
	pairOK(t, err)
	if len(pending) == 0 {
		t.Fatal("active pairing has no completion to deliver")
	}
}
