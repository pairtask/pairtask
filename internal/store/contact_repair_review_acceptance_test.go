package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/review"
)

// repairReviewEpochFixture represents an installed second pairing in the
// store. It does not exercise or claim to verify the pairing handshake.
func repairReviewEpochFixture(t *testing.T, c *consentPublishCase) {
	t.Helper()
	unpairPublicationContact(t, c)
	op := &operatorContactFixture{db: c.f.db, raw: c.f.raw, contact: c.f.key}
	o := op.epochOutcome(t, 111)
	_, err := c.f.db.Commit(context.Background(), o)
	pairOK(t, err)
	op.deliverEpoch(t, o)
	// Explicit new authority in epoch 2 must never authorize epoch 1 work.
	_, err = c.f.raw.Exec(`INSERT INTO peer_review_grants(contact,pairing_epoch,agent,mode,publish,formal,granted_at)
 VALUES (?,2,'reviewer','auto',1,1,'2026-11-06T00:00:00Z')`, c.f.key)
	pairOK(t, err)
}

func TestContactRepairReviewAcceptanceOldReviewCannotUseNewPairingAuthority(t *testing.T) {
	c := newConsentPublishCase(t, protocol.ConsentAlways, true)
	c.decide(t)
	c.dispatch(t)
	repairReviewEpochFixture(t, c)
	c.saveVerdict(t)
	before, found, err := c.f.db.GetReview(context.Background(), c.reviewID)
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, errors.Is(c.reserve(), review.ErrDenied), true)
	after, _, err := c.f.db.GetReview(context.Background(), c.reviewID)
	pairOK(t, err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("old review borrowed the new pairing's publication authority")
	}
}

func TestContactRepairReviewAcceptanceOldCompletionDoesNotChangeReusedAsk(t *testing.T) {
	c := newConsentPublishCase(t, protocol.ConsentOnce, true)
	c.decide(t)
	c.dispatch(t)
	repairReviewEpochFixture(t, c)
	_, err := c.f.raw.Exec(`INSERT INTO review_outcome_seq(contact,pairing_epoch,ask_id,reviewer,seq,created_at)
 VALUES (?,2,?,'reviewer',41,'2026-11-06T00:00:00Z')`, c.f.key, c.m.ID)
	pairOK(t, err)
	_, err = c.f.raw.Exec(`INSERT INTO review_outcomes_out(contact,pairing_epoch,ask_id,seq,payload,queued_at)
 VALUES (?,2,?,41,'{"canary":"new-pairing-outcome"}','2026-11-06T00:00:00Z')`, c.f.key, c.m.ID)
	pairOK(t, err)
	seq := epochRows(t, c.f.raw, "review_outcome_seq", epochColumns(t, c.f.raw, "review_outcome_seq"))
	out := epochRows(t, c.f.raw, "review_outcomes_out", epochColumns(t, c.f.raw, "review_outcomes_out"))
	c.saveVerdict(t)
	rec, found, err := c.f.db.GetReview(context.Background(), c.reviewID)
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, rec.View.State, protocol.ReviewCompleted)
	if !reflect.DeepEqual(seq, epochRows(t, c.f.raw, "review_outcome_seq", epochColumns(t, c.f.raw, "review_outcome_seq"))) {
		t.Error("old completion changed outcome numbering across pairing epochs")
	}
	if !reflect.DeepEqual(out, epochRows(t, c.f.raw, "review_outcomes_out", epochColumns(t, c.f.raw, "review_outcomes_out"))) {
		t.Error("old completion replaced new-pairing outcome for reused remote ask ID")
	}
}
