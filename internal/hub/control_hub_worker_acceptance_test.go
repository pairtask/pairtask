package hub

import (
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/store"
)

func TestControlHubWorkerAcceptanceLocalPauseImmediatelyDialsNonDialingSide(t *testing.T) {
	w := newControlWorkerFixture(t)
	w.run(t)
	gen := w.change(t, "pause", "control-immediate")
	messageSyncEqual(t, awaitMessageWorkerDial(t, w.started), "peer-1:443")
	w.attempts(t, gen, 1)
}
func TestControlHubWorkerAcceptanceExactlyFiveAttemptsNoSixth(t *testing.T) {
	w := newControlWorkerFixture(t)
	w.run(t)
	gen := w.change(t, "pause", "control-budget")
	awaitMessageWorkerDial(t, w.started)
	w.attempts(t, gen, 1)
	for count := 2; count <= 5; count++ {
		w.advance(3600)
		awaitMessageWorkerDial(t, w.started)
		w.attempts(t, gen, count)
	}
	var state string
	messageWorkerOK(t, w.raw.QueryRow(`SELECT state FROM contact_notices_out WHERE contact=? AND gen=?`, w.key, gen).Scan(&state))
	messageSyncEqual(t, state, "undelivered")
	w.advance(3600)
	w.noDial(t)
}
func TestControlHubWorkerAcceptanceReplacementWakeCannotBypassMinuteBound(t *testing.T) {
	w := newControlWorkerFixture(t)
	w.run(t)
	first := w.change(t, "pause", "control-first")
	awaitMessageWorkerDial(t, w.started)
	w.attempts(t, first, 1)
	next := w.change(t, "resume", "control-replacement")
	w.advance(0)
	w.noDial(t)
	w.advance(61)
	awaitMessageWorkerDial(t, w.started)
	w.attempts(t, next, 1)
	w.attempts(t, first, 1)
}
func TestControlHubWorkerAcceptanceMessageClaimExcludesControlPass(t *testing.T) {
	w := newControlWorkerFixture(t)
	w.run(t)
	end, ok := w.h.beginMessages(w.key)
	messageSyncEqual(t, ok, true)
	gen := w.change(t, "pause", "control-claim")
	w.noDial(t)
	end(false)
	w.advance(0)
	awaitMessageWorkerDial(t, w.started)
	w.attempts(t, gen, 1)
}
func (w *controlWorkerFixture) unpair(t *testing.T) int64 {
	t.Helper()
	audit, err := w.h.store.RecordOperatorAudit(context.Background(), store.OperatorAudit{Action: "contact unpair", MessageID: "control-startup-unpair", Scope: w.key, Result: store.AuditPending})
	messageWorkerOK(t, err)
	out, _, err := w.h.store.UnpairContact(context.Background(), store.ContactUnpair{OperationID: "control-startup-unpair", Contact: w.key, Confirm: w.key, SeenEpoch: 1, AuditID: audit, At: time.Now()})
	messageWorkerOK(t, err)
	return out.Generation
}
func TestControlHubWorkerAcceptanceUnpairNoticeRunsAtStartup(t *testing.T) {
	w := newControlWorkerFixture(t)
	gen := w.unpair(t)
	w.run(t)
	awaitMessageWorkerDial(t, w.started)
	w.attempts(t, gen, 1)
}
func TestControlHubWorkerAcceptanceNoAddressStaysPendingWithoutAttempt(t *testing.T) {
	w := newControlWorkerFixture(t)
	_, err := w.raw.Exec(`UPDATE peer_contacts SET destination='' WHERE identity_key=?`, w.key)
	messageWorkerOK(t, err)
	gen := w.unpair(t)
	w.run(t)
	w.noDial(t)
	w.attempts(t, gen, 0)
	var state string
	messageWorkerOK(t, w.raw.QueryRow(`SELECT state FROM contact_notices_out WHERE contact=? AND gen=?`, w.key, gen).Scan(&state))
	messageSyncEqual(t, state, "pending")
}
