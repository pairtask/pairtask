package store_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func TestContactRepairImportAcceptanceReviewUsesFrozenNewSource(t *testing.T) {
	c := newConsentPublishCase(t, protocol.ConsentAlways, true)
	c.decide(t)
	c.dispatch(t)
	old, found, err := c.f.db.GetReview(context.Background(), c.reviewID)
	pairOK(t, err)
	pairEqual(t, found, true)
	namespaceRepair(t, c.f)
	seq := namespaceEpochRows(t, c.f, "review_outcome_seq", 1)
	outcomes := namespaceEpochRows(t, c.f, "review_outcomes_out", 1)
	_, err = c.f.db.PeerMessages().SetReviewGrant(context.Background(), c.f.key, store.ReviewGrantChange{Agents: []string{"reviewer"}, Mode: store.ReviewAuto, Publish: true, Formal: true, Now: messageAt})
	pairOK(t, err)
	next := c.m
	next.Text = "new pairing review"
	namespaceAccept(t, c.f, next)
	pending := namespacePending(t, c.f, next.ID)
	pairEqual(t, pending.Epoch, int64(2))
	result, err := c.f.db.ImportRemote(context.Background(), pending, 21, messageAt)
	pairOK(t, err)
	pairEqual(t, result.State, store.ImportImported)
	if result.ReviewID == c.reviewID {
		t.Fatal("new epoch reused retired review ID")
	}
	var source int64
	pairOK(t, c.f.raw.QueryRow(`SELECT remote_epoch FROM reviews WHERE review_id=?`, result.ReviewID).Scan(&source))
	pairEqual(t, source, int64(2))
	prior, _, err := c.f.db.GetReview(context.Background(), c.reviewID)
	pairOK(t, err)
	if !reflect.DeepEqual(old, prior) {
		t.Error("new import changed retired review")
	}
	if !reflect.DeepEqual(seq, namespaceEpochRows(t, c.f, "review_outcome_seq", 1)) {
		t.Error("new ask changed retired outcome numbering")
	}
	if !reflect.DeepEqual(outcomes, namespaceEpochRows(t, c.f, "review_outcomes_out", 1)) {
		t.Error("new ask changed retired outcomes")
	}
}

func TestContactRepairImportAcceptanceOldConsentIsNotReplayedInNewEpoch(t *testing.T) {
	f := consentFixture(t, "recipient")
	m := consentMessage("reused-consent-id", 1, "recipient")
	old := consentHeld(t, f, m, messageAt)
	_, err := f.db.DecideConsent(context.Background(), consentDecision(old, protocol.ConsentOnce, messageAt))
	pairOK(t, err)
	namespaceRepair(t, f)
	a, err := f.consentAccept(t, 8, m, messageAt)
	pairOK(t, err)
	if !a.Stored || !a.Held || a.HeldID == "" || a.HeldID == old.ID {
		t.Fatalf("new pairing needs its own consent hold: %+v", a)
	}
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM consent_requests WHERE pairing_epoch=2 AND state='pending'`), 1)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM consent_requests WHERE pairing_epoch=1 AND id=? AND state='allowed'`, old.ID), 1)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM remote_imports WHERE pairing_epoch=2 AND state='pending'`), 0)
}
