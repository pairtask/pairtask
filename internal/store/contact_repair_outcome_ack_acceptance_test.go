package store_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func repairOutcomeAckFixture(t *testing.T) *consentPublishCase {
	t.Helper()
	c := newConsentPublishCase(t, protocol.ConsentOnce, true)
	c.decide(t)
	c.dispatch(t)
	old, err := json.Marshal(messaging.ReviewOutcome{AskID: c.m.ID, Reviewer: "reviewer", Seq: 500, State: "requested"})
	pairOK(t, err)
	_, err = c.f.raw.Exec(`UPDATE review_outcome_seq SET seq=500 WHERE contact=? AND pairing_epoch=1 AND ask_id=?`, c.f.key, c.m.ID)
	pairOK(t, err)
	_, err = c.f.raw.Exec(`UPDATE review_outcomes_out SET seq=500,payload=? WHERE contact=? AND pairing_epoch=1 AND ask_id=?`, string(old), c.f.key, c.m.ID)
	pairOK(t, err)
	repairReviewEpochFixture(t, c)
	_, err = c.f.raw.Exec(`UPDATE peer_pairing_requests SET state='accepted',delivered_at='2026-11-06T00:00:00Z' WHERE identity_key=?`, c.f.key)
	pairOK(t, err)
	fresh, err := json.Marshal(messaging.ReviewOutcome{AskID: c.m.ID, Reviewer: "reviewer", Seq: 41, State: "requested"})
	pairOK(t, err)
	_, err = c.f.raw.Exec(`INSERT INTO review_outcome_seq(contact,pairing_epoch,ask_id,reviewer,seq,created_at) VALUES (?,2,?,'reviewer',41,'2026-11-06T00:00:00Z')`, c.f.key, c.m.ID)
	pairOK(t, err)
	_, err = c.f.raw.Exec(`INSERT INTO review_outcomes_out(contact,pairing_epoch,ask_id,seq,payload,queued_at) VALUES (?,2,?,41,?,'2026-11-06T00:00:00Z')`, c.f.key, c.m.ID, string(fresh))
	pairOK(t, err)
	return c
}

func retiredOutcomeAckEvidence(t *testing.T, c *consentPublishCase) [][]string {
	t.Helper()
	return [][]string{
		epochQueryStrings(t, c.f.raw, `SELECT json_array(seq,acked_seq,refused,terminal_seq) FROM review_outcome_seq WHERE pairing_epoch=1 ORDER BY contact,ask_id`),
		epochQueryStrings(t, c.f.raw, `SELECT json_array(seq,payload,queued_at) FROM review_outcomes_out WHERE pairing_epoch=1 ORDER BY contact,ask_id`),
	}
}

func TestContactRepairOutcomeAckAcceptanceNewSessionSeesOnlyItsEpoch(t *testing.T) {
	c := repairOutcomeAckFixture(t)
	bound, err := c.f.db.PeerMessages().Admit(context.Background(), c.f.key)
	pairOK(t, err)
	pending, err := bound.PendingOutcomes(context.Background(), c.f.key, 10)
	pairOK(t, err)
	if len(pending) != 1 {
		t.Fatalf("new session sees %d outcomes; want only its epoch's outcome", len(pending))
	}
	pairEqual(t, pending[0].Seq, uint64(41))
	pairEqual(t, pending[0].ReviewID, "") // No review has been imported for this epoch's ask.
}

func TestContactRepairOutcomeAckAcceptanceFutureAckCannotUseRetiredSequence(t *testing.T) {
	c := repairOutcomeAckFixture(t)
	bound, err := c.f.db.PeerMessages().Admit(context.Background(), c.f.key)
	pairOK(t, err)
	seq := epochRows(t, c.f.raw, "review_outcome_seq", epochColumns(t, c.f.raw, "review_outcome_seq"))
	out := epochRows(t, c.f.raw, "review_outcomes_out", epochColumns(t, c.f.raw, "review_outcomes_out"))
	pairOK(t, bound.OutcomeAnswered(context.Background(), c.f.key, messaging.OutcomeReply{AskID: c.m.ID, Seq: 100}))
	if !reflect.DeepEqual(seq, epochRows(t, c.f.raw, "review_outcome_seq", epochColumns(t, c.f.raw, "review_outcome_seq"))) {
		t.Error("future ACK validated against or changed another pairing's sequence")
	}
	if !reflect.DeepEqual(out, epochRows(t, c.f.raw, "review_outcomes_out", epochColumns(t, c.f.raw, "review_outcomes_out"))) {
		t.Error("future ACK dropped a queued outcome using retired sequence authority")
	}
}

func TestContactRepairOutcomeAckAcceptanceStoredAckChangesOnlyCurrentEpoch(t *testing.T) {
	c := repairOutcomeAckFixture(t)
	bound, err := c.f.db.PeerMessages().Admit(context.Background(), c.f.key)
	pairOK(t, err)
	retired := retiredOutcomeAckEvidence(t, c)
	pairOK(t, bound.OutcomeAnswered(context.Background(), c.f.key, messaging.OutcomeReply{AskID: c.m.ID, Seq: 41}))
	pairEqual(t, epochCount(t, c.f.raw, `SELECT count(*) FROM review_outcomes_out WHERE pairing_epoch=2`), 0)
	pairEqual(t, epochCount(t, c.f.raw, `SELECT acked_seq FROM review_outcome_seq WHERE pairing_epoch=2`), 41)
	if !reflect.DeepEqual(retired, retiredOutcomeAckEvidence(t, c)) {
		t.Fatal("current pairing ACK changed retired outcome or numbering evidence")
	}
}
