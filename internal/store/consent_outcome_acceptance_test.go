package store_test

import (
	"context"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"testing"
)

type consentOutcomeQueue interface {
	PendingConsentOutcomes(context.Context, string, int) ([]messaging.ConsentOutcome, error)
	ConsentOutcomeAnswered(context.Context, string, messaging.ConsentOutcomeStored) error
}

func consentQueuedOutcomes(t *testing.T, f workspaceStoreFixture) consentOutcomeQueue {
	t.Helper()
	q, ok := any(f.db.PeerMessages()).(consentOutcomeQueue)
	if !ok {
		t.Fatal("durable consent outcome queue not implemented")
	}
	return q
}
func consentStoreReopen(t *testing.T, f workspaceStoreFixture) workspaceStoreFixture {
	t.Helper()
	var seq int
	var name, path string
	pairOK(t, f.raw.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path))
	pairOK(t, f.db.Close())
	f.db = pairSQLite(t, path)
	return f
}

func TestConsentOutcomeStoreAcceptanceOfflineExpiryPersistsOriginalID(t *testing.T) {
	f := consentFixture(t, "alpha")
	m := consentMessage("original-expiring-request", 1, "alpha")
	v := consentHeld(t, f, m, messageAt)
	q := consentQueuedOutcomes(t, f)
	before, err := q.PendingConsentOutcomes(context.Background(), f.key, 20)
	pairOK(t, err)
	pairEqual(t, len(before), 0)
	count, err := f.db.ExpireConsent(context.Background(), v.ExpiresAt)
	pairOK(t, err)
	pairEqual(t, count, 1)
	f = consentStoreReopen(t, f)
	q = consentQueuedOutcomes(t, f)
	outcomes, err := q.PendingConsentOutcomes(context.Background(), f.key, 20)
	pairOK(t, err)
	pairEqual(t, len(outcomes), 1)
	pairEqual(t, outcomes[0].ID, m.ID)
	pairEqual(t, outcomes[0].State, "expired")
	pairOK(t, q.ConsentOutcomeAnswered(context.Background(), f.key, messaging.ConsentOutcomeStored{ID: "unrelated-request"}))
	again, err := q.PendingConsentOutcomes(context.Background(), f.key, 20)
	pairOK(t, err)
	pairEqual(t, len(again), 1)
	pairEqual(t, again[0], outcomes[0])
}

func TestConsentOutcomeStoreAcceptanceDeclineAndAckReplayAreDurable(t *testing.T) {
	f := consentFixture(t, "alpha")
	m := consentMessage("original-declined-request", 1, "alpha")
	v := consentHeld(t, f, m, messageAt)
	d := consentDecision(v, protocol.ConsentDecline, messageAt)
	d.AuditID = consentAudit(t, f, "consent_decide")
	_, err := f.db.DecideConsent(context.Background(), d)
	pairOK(t, err)
	f = consentStoreReopen(t, f)
	q := consentQueuedOutcomes(t, f)
	outcomes, err := q.PendingConsentOutcomes(context.Background(), f.key, 20)
	pairOK(t, err)
	pairEqual(t, len(outcomes), 1)
	pairEqual(t, outcomes[0].ID, m.ID)
	pairEqual(t, outcomes[0].State, "declined")
	ack := messaging.ConsentOutcomeStored{ID: m.ID}
	pairOK(t, q.ConsentOutcomeAnswered(context.Background(), f.key, ack))
	pairOK(t, q.ConsentOutcomeAnswered(context.Background(), f.key, ack))
	outcomes, err = q.PendingConsentOutcomes(context.Background(), f.key, 20)
	pairOK(t, err)
	pairEqual(t, len(outcomes), 0)
}

func TestConsentOutcomeStoreAcceptanceApprovalIsNotDispatchOutcome(t *testing.T) {
	f := consentFixture(t, "alpha")
	v := consentHeld(t, f, consentMessage("queued-session", 1, "alpha"), messageAt)
	d := consentDecision(v, protocol.ConsentSession, messageAt)
	d.AuditID = consentAudit(t, f, "consent_decide")
	_, err := f.db.DecideConsent(context.Background(), d)
	pairOK(t, err)
	outcomes, err := consentQueuedOutcomes(t, f).PendingConsentOutcomes(context.Background(), f.key, 20)
	pairOK(t, err)
	pairEqual(t, len(outcomes), 0)
}
