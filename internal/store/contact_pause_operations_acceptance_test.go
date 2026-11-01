package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func pauseRequest(t *testing.T, f *operatorContactFixture, id string, paused bool) store.ContactPause {
	t.Helper()
	op := "resume"
	if paused {
		op = "pause"
	}
	return store.ContactPause{OperationID: id, Contact: f.contact, Paused: paused, At: messageAt, AuditID: f.audit(t, contactAuditInput{op, id})}
}

func pauseApply(t *testing.T, f *operatorContactFixture, req store.ContactPause) store.ContactPauseResult {
	t.Helper()
	r, err := f.db.SetContactPaused(context.Background(), req)
	pairOK(t, err)
	return r
}

func TestContactPauseOperationsAcceptanceReplaySurvivesResumeAndRestart(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	pause := pauseRequest(t, f, "pause-op", true)
	first := pauseApply(t, f, pause)
	pairEqual(t, first.Changed, true)
	pairEqual(t, first.Generation, int64(1))
	pairEqual(t, first.PausedAt.Equal(messageAt), true)
	pairEqual(t, pauseApply(t, f, pause), first)
	resume := pauseApply(t, f, pauseRequest(t, f, "resume-op", false))
	pairEqual(t, resume.Generation, int64(2))
	pairEqual(t, resume.PausedAt.IsZero(), true)
	pairOK(t, f.db.Close())
	f.db = pairSQLite(t, f.path)
	pairEqual(t, pauseApply(t, f, pause), first)
	c := contactGet(t, f.db, f.contact)
	pairEqual(t, c.Generation, int64(2))
	pairEqual(t, c.PausedAt.IsZero(), true)
	retry := pause
	retry.AuditID = f.audit(t, contactAuditInput{"pause", pause.OperationID})
	pairEqual(t, pauseApply(t, f, retry), first)
	var replayDetail string
	pairOK(t, f.raw.QueryRow("SELECT detail FROM operator_audit WHERE id=?", retry.AuditID).Scan(&replayDetail))
	pairEqual(t, replayDetail, "pause (replayed)")
	other := messageContact(t, f.db, 2)
	for _, altered := range []store.ContactPause{
		{OperationID: pause.OperationID, Contact: f.contact, Paused: false, AuditID: pause.AuditID, At: messageAt},
		{OperationID: pause.OperationID, Contact: other, Paused: true, AuditID: pause.AuditID, At: messageAt},
	} {
		_, err := f.db.SetContactPaused(ctx, altered)
		pairEqual(t, errors.Is(err, messaging.ErrConflict), true)
	}
}

func TestContactPauseOperationsAcceptanceNoOpsKeepAdmittedGeneration(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	active, err := f.db.PeerMessages().Admit(ctx, f.contact)
	pairOK(t, err)
	noop := pauseApply(t, f, pauseRequest(t, f, "already-active", false))
	pairEqual(t, noop.Changed, false)
	pairEqual(t, noop.Generation, int64(0))
	_, err = active.Offer(ctx, f.contact, messageAt, messaging.SessionLimit)
	pairOK(t, err)
	paused := pauseApply(t, f, pauseRequest(t, f, "pause", true))
	noop = pauseApply(t, f, pauseRequest(t, f, "already-paused", true))
	pairEqual(t, noop.Changed, false)
	pairEqual(t, noop.Generation, paused.Generation)
	pairEqual(t, noop.PausedAt, paused.PausedAt)
	pauseApply(t, f, pauseRequest(t, f, "resume", false))
	active, err = f.db.PeerMessages().Admit(ctx, f.contact)
	pairOK(t, err)
	pauseApply(t, f, pauseRequest(t, f, "still-active", false))
	_, err = active.SharedAgents(ctx, f.contact)
	pairOK(t, err)
	for id, detail := range map[string]string{"already-active": "already active", "already-paused": "already paused", "pause": "pause", "resume": "resume"} {
		var result, got string
		pairOK(t, f.raw.QueryRow("SELECT result,detail FROM operator_audit WHERE message_id=?", id).Scan(&result, &got))
		pairEqual(t, result, store.AuditAccepted)
		pairEqual(t, got, detail)
	}
}

func TestContactPauseOperationsAcceptanceAuditFailureRollsBackChangesAndNoOps(t *testing.T) {
	for _, tc := range []struct {
		name            string
		initial, target bool
	}{
		{"pause", false, true}, {"resume", true, false}, {"noop-pause", true, true}, {"noop-resume", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOperatorContactFixture(t)
			if tc.initial {
				pauseApply(t, f, pauseRequest(t, f, "initial-pause", true))
			}
			before := contactGet(t, f.db, f.contact)
			req := pauseRequest(t, f, "atomic-operation", tc.target)
			_, err := f.raw.Exec("CREATE TRIGGER fail_pause_audit BEFORE UPDATE ON operator_audit WHEN NEW.result='accepted' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END")
			pairOK(t, err)
			_, err = f.db.SetContactPaused(context.Background(), req)
			if err == nil {
				t.Fatal("pause operation survived audit failure")
			}
			pairEqual(t, contactGet(t, f.db, f.contact), before)
			var count int
			pairOK(t, f.raw.QueryRow("SELECT COUNT(*) FROM operator_contact_requests WHERE operation_id=?", req.OperationID).Scan(&count))
			pairEqual(t, count, 0)
			pairOK(t, f.raw.QueryRow("SELECT COUNT(*) FROM contact_pause_results WHERE operation_id=?", req.OperationID).Scan(&count))
			pairEqual(t, count, 0)
			_, err = f.raw.Exec("DROP TRIGGER fail_pause_audit")
			pairOK(t, err)
			pauseApply(t, f, req)
		})
	}
}

func TestContactPauseOperationsAcceptanceUnconfirmedCannotPause(t *testing.T) {
	f := newOperatorContactFixture(t)
	r := contactReady(t, f.db, "unconfirmed-pause")
	destinationAccept(t, f.db, r, joinAt)
	req := pauseRequest(t, f, "unconfirmed", true)
	req.Contact = r.Peer
	_, err := f.db.SetContactPaused(context.Background(), req)
	pairEqual(t, errors.Is(err, messaging.ErrUnknownContact), true)
}

func TestContactPauseOperationsAcceptancePausedStateSurvivesRestart(t *testing.T) {
	f := newOperatorContactFixture(t)
	first := pauseApply(t, f, pauseRequest(t, f, "durable-pause", true))
	pairOK(t, f.db.Close())
	f.db = pairSQLite(t, f.path)
	c := contactGet(t, f.db, f.contact)
	pairEqual(t, c.Generation, first.Generation)
	pairEqual(t, c.PausedAt, first.PausedAt)
	_, err := f.db.PeerMessages().Admit(context.Background(), f.contact)
	pairEqual(t, errors.Is(err, store.ErrPaused), true)
}
