package store_test

import (
	"context"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
	"testing"
	"time"
)

type consentVersionAcceptor interface {
	AcceptVersion(context.Context, string, int, messaging.Message, time.Time) (messaging.Answer, error)
}
type consentLegacyAcceptor interface {
	Accept(context.Context, string, messaging.Message, time.Time) (messaging.Answer, error)
}

func consentAccept(t *testing.T, db *store.Store, key string, version int, m messaging.Message, now time.Time) (messaging.Answer, error) {
	t.Helper()
	a := any(db.PeerMessages())
	if s, ok := a.(consentVersionAcceptor); ok {
		return s.AcceptVersion(context.Background(), key, version, m, now)
	}
	if s, ok := a.(consentLegacyAcceptor); ok {
		return s.Accept(context.Background(), key, m, now)
	}
	t.Fatal("missing inbound acceptance seam")
	return messaging.Answer{}, nil
}
func consentAudit(t *testing.T, f workspaceStoreFixture, op string) int64 {
	t.Helper()
	id, err := f.db.RecordOperatorAudit(context.Background(), store.OperatorAudit{Action: "contact " + op, Scope: f.key, Result: store.AuditPending})
	pairOK(t, err)
	return id
}
func consentFixture(t *testing.T, agents ...string) workspaceStoreFixture {
	t.Helper()
	f := workspaceStore(t)
	routingRegister(t, f.db, agents...)
	pairOK(t, f.db.SetConsentOptIn(context.Background(), f.key, true, consentAudit(t, f, "consent_opt"), messageAt))
	for _, agent := range agents {
		pairOK(t, f.db.ResetConsentScope(context.Background(), f.key, agent, protocol.ConsentMessage, consentAudit(t, f, "consent_reset"), messageAt))
	}
	return f
}
func consentMessage(id string, n uint64, agents ...string) messaging.Message {
	m := messageWire(id, n)
	m.From = "claimed-sender"
	m.To = agents
	return m
}
func consentHeld(t *testing.T, f workspaceStoreFixture, m messaging.Message, now time.Time) protocol.ConsentView {
	t.Helper()
	a, err := consentAccept(t, f.db, f.key, 8, m, now)
	pairOK(t, err)
	if !a.Held || a.HeldID == "" || !a.Stored {
		t.Fatalf("request escaped or was refused instead of held: %+v", a)
	}
	page, err := f.db.ConsentPage(context.Background(), f.key, 0, 100)
	pairOK(t, err)
	for _, v := range page.Items {
		if v.ID == a.HeldID {
			return v
		}
	}
	t.Fatal("held request missing from dedicated page")
	return protocol.ConsentView{}
}
func consentDecision(v protocol.ConsentView, choice string, now time.Time) store.ConsentDecision {
	return store.ConsentDecision{ID: v.ID, Digest: v.Digest, Revision: v.Revision, Choice: choice, Evidence: store.ConsentEvidence{Kind: choice, Epoch: "broker-epoch", Gen: 7}, Now: now}
}
func consentSnapshot(t *testing.T, f workspaceStoreFixture) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range []string{"consent_requests", "consent_decisions", "consent_rate", "remote_imports", "peer_messages", "messages", "durable_inbox", "operator_audit", "peer_grants", "peer_review_grants"} {
		out[table] = reviewIDTable(t, f, "SELECT * FROM "+table+" ORDER BY rowid")
	}
	return out
}
func consentUnchanged(t *testing.T, f workspaceStoreFixture, before map[string]string) {
	t.Helper()
	after := consentSnapshot(t, f)
	for table, want := range before {
		pairEqual(t, after[table], want)
	}
}

func consentAnotherContact(t *testing.T, f workspaceStoreFixture, n int, agents ...string) workspaceStoreFixture {
	t.Helper()
	f.key = messageContact(t, f.db, n)
	pairOK(t, f.db.SetConsentOptIn(context.Background(), f.key, true, consentAudit(t, f, "consent_opt"), messageAt))
	for _, agent := range agents {
		routingRegister(t, f.db, agent)
		pairOK(t, f.db.ResetConsentScope(context.Background(), f.key, agent, protocol.ConsentMessage, consentAudit(t, f, "consent_reset"), messageAt))
	}
	return f
}
