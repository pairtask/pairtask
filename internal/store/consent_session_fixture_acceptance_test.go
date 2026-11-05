package store_test

import (
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

type consentSessionCase struct {
	f     workspaceStoreFixture
	owner consentOwnerControl
	epoch string
	gen   uint64
}

func newConsentSessionCase(t *testing.T) *consentSessionCase {
	t.Helper()
	f := consentDeclineFixture(t, true)
	c := &consentSessionCase{f: f, owner: consentRequireOwner(t, f), epoch: "broker-epoch", gen: 7}
	pairOK(t, c.owner.SetConsentOwner(context.Background(), c.epoch, c.gen))
	return c
}
func (c *consentSessionCase) approve(t *testing.T, m messaging.Message) {
	t.Helper()
	v := consentHeld(t, c.f, m, messageAt)
	d := consentDecision(v, protocol.ConsentSession, messageAt)
	d.Evidence.Epoch, d.Evidence.Gen = c.epoch, c.gen
	_, err := c.f.db.DecideConsent(context.Background(), d)
	pairOK(t, err)
}
func (c *consentSessionCase) admitted(t *testing.T, m messaging.Message) {
	t.Helper()
	// A new wrapper must share the live owner's fail-closed gate.
	a := any(c.f.db.PeerMessages()).(consentVersionAcceptor)
	answer, err := a.AcceptVersion(context.Background(), c.f.key, 8, m, messageAt)
	pairOK(t, err)
	pairEqual(t, answer.Stored, true)
	page, err := c.f.db.ConsentPage(context.Background(), c.f.key, 0, 100)
	pairOK(t, err)
	consentAssertNoRepeatedPrompt(t, page.Items, answer.HeldID)
	pending, err := c.f.db.PendingImports(context.Background(), 50)
	pairOK(t, err)
	for _, imp := range pending {
		if imp.RemoteID == m.ID {
			pairEqual(t, imp.Consent, store.ConsentEvidence{Kind: protocol.ConsentSession, Epoch: c.epoch, Gen: c.gen})
			return
		}
	}
	t.Fatal("session-admitted request was not queued with session evidence")
}
func (c *consentSessionCase) failWrites(t *testing.T, enabled bool) {
	t.Helper()
	for _, action := range []string{"INSERT", "UPDATE", "DELETE"} {
		query := "DROP TRIGGER consent_acceptance_fail_" + action
		if enabled {
			query = "CREATE TRIGGER consent_acceptance_fail_" + action + " BEFORE " + action + " ON consent_owner BEGIN SELECT RAISE(ABORT, 'acceptance owner write failure'); END"
		}
		_, err := c.f.raw.Exec(query)
		pairOK(t, err)
	}
}
func (c *consentSessionCase) oldOwnerRemains(t *testing.T) {
	t.Helper()
	var epoch string
	var gen uint64
	pairOK(t, c.f.raw.QueryRow(`SELECT epoch, gen FROM consent_owner WHERE id = 1`).Scan(&epoch, &gen))
	pairEqual(t, epoch, c.epoch)
	pairEqual(t, gen, c.gen)
}

func consentAssertNoRepeatedPrompt(t *testing.T, items []protocol.ConsentView, heldID string) {
	t.Helper()
	for _, v := range items {
		if v.ID == heldID {
			t.Fatal("session reuse prompted for an already covered request")
		}
	}
}
