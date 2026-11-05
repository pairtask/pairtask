package store_test

import (
	"context"
	"errors"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
	"testing"
	"time"
)

func TestConsentDecisionAcceptanceLaterNeverExtendsExpiryOrGrants(t *testing.T) {
	f := consentFixture(t, "alpha")
	v := consentHeld(t, f, consentMessage("later", 1, "alpha"), messageAt)
	d := consentDecision(v, protocol.ConsentLater, messageAt.Add(time.Hour))
	d.AuditID = consentAudit(t, f, "consent_decide")
	out, err := f.db.DecideConsent(context.Background(), d)
	pairOK(t, err)
	pairEqual(t, out.Dispatched, false)
	page, err := f.db.ConsentPage(context.Background(), f.key, 0, 20)
	pairOK(t, err)
	pairEqual(t, page.Total, 1)
	pairEqual(t, page.Items[0].ExpiresAt.Equal(v.ExpiresAt), true)
	next := consentMessage("still-undecided", 2, "alpha")
	consentHeld(t, f, next, messageAt.Add(2*time.Hour))
	pending, err := f.db.PendingImports(context.Background(), 20)
	pairOK(t, err)
	pairEqual(t, len(pending), 0)
}

func TestConsentDecisionAcceptanceExpiryIsDurableAndTerminal(t *testing.T) {
	f := consentFixture(t, "alpha")
	m := consentMessage("expiry", 1, "alpha")
	v := consentHeld(t, f, m, messageAt)
	count, err := f.db.ExpireConsent(context.Background(), v.ExpiresAt)
	pairOK(t, err)
	pairEqual(t, count, 1)
	count, err = f.db.ExpireConsent(context.Background(), v.ExpiresAt.Add(time.Hour))
	pairOK(t, err)
	pairEqual(t, count, 0)
	d := consentDecision(v, protocol.ConsentOnce, v.ExpiresAt.Add(time.Second))
	out, err := f.db.DecideConsent(context.Background(), d)
	if err == nil && out.Dispatched {
		t.Fatal("expired request dispatched")
	}
	a, err := consentAccept(t, f.db, f.key, 8, m, v.ExpiresAt.Add(2*time.Hour))
	pairOK(t, err)
	if a.Held {
		t.Fatalf("terminal expiry reopened: %+v", a)
	}
	pending, err := f.db.PendingImports(context.Background(), 20)
	pairOK(t, err)
	pairEqual(t, len(pending), 0)
}

func TestConsentDecisionAcceptanceOnceStagesWholeRequestExactlyOnce(t *testing.T) {
	f := consentFixture(t, "alpha", "beta")
	v := consentHeld(t, f, consentMessage("once", 1, "alpha", "beta"), messageAt)
	d := consentDecision(v, protocol.ConsentOnce, messageAt)
	d.AuditID = consentAudit(t, f, "consent_decide")
	out, err := f.db.DecideConsent(context.Background(), d)
	pairOK(t, err)
	pairEqual(t, out.Dispatched, true)
	pending, err := f.db.PendingImports(context.Background(), 20)
	pairOK(t, err)
	pairEqual(t, len(pending), 1)
	before := consentSnapshot(t, f)
	again, err := f.db.DecideConsent(context.Background(), d)
	if err != nil && !errors.Is(err, store.ErrConsentStale) {
		t.Fatal(err)
	}
	if err == nil && again.ID != out.ID {
		t.Fatal("decision replay changed identity")
	}
	consentUnchanged(t, f, before)
	consentHeld(t, f, consentMessage("fresh-needs-consent", 2, "alpha", "beta"), messageAt.Add(time.Minute))
}

func TestConsentDecisionAcceptanceFailureRollsBackEvidenceAuditAndDispatch(t *testing.T) {
	f := consentFixture(t, "alpha", "beta")
	v := consentHeld(t, f, consentMessage("atomic", 1, "alpha", "beta"), messageAt)
	d := consentDecision(v, protocol.ConsentAlways, messageAt)
	d.AuditID = consentAudit(t, f, "consent_decide")
	_, err := f.raw.Exec(`CREATE TRIGGER fail_consent_dispatch BEFORE INSERT ON remote_imports BEGIN SELECT RAISE(ABORT,'injected consent dispatch failure'); END`)
	pairOK(t, err)
	before := consentSnapshot(t, f)
	if _, err = f.db.DecideConsent(context.Background(), d); err == nil {
		t.Fatal("injected dispatch failure succeeded")
	}
	consentUnchanged(t, f, before)
	_, err = f.raw.Exec(`DROP TRIGGER fail_consent_dispatch`)
	pairOK(t, err)
	out, err := f.db.DecideConsent(context.Background(), d)
	pairOK(t, err)
	pairEqual(t, out.Dispatched, true)
	pending, err := f.db.PendingImports(context.Background(), 20)
	pairOK(t, err)
	pairEqual(t, len(pending), 1)
}
