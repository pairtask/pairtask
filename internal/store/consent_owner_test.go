package store_test

import (
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// sessionDecide allows a held request for the session (epoch, gen).
func sessionDecide(t *testing.T, f workspaceStoreFixture, v protocol.ConsentView, gen uint64) {
	t.Helper()
	d := consentDecision(v, protocol.ConsentSession, messageAt)
	d.Evidence.Epoch, d.Evidence.Gen = "epoch", gen
	_, err := f.db.DecideConsent(context.Background(), d)
	pairOK(t, err)
}

// arrival accepts message n to alpha and reports whether it waits for the
// operator (true) or was admitted under a session (false).
func arrival(t *testing.T, f workspaceStoreFixture, id string, n uint64) bool {
	t.Helper()
	a, err := f.consentAccept(t, 8, consentMessage(id, n, "alpha"), messageAt)
	pairOK(t, err)
	if !a.Held {
		t.Fatalf("%s not held or admitted: %+v", id, a)
	}
	page, err := f.db.ConsentPage(context.Background(), f.key, 0, 100)
	pairOK(t, err)
	for _, v := range page.Items {
		if v.ID == a.HeldID {
			return true
		}
	}
	return false
}

// stagedUnder is the evidence an admitted request was staged under.
func stagedUnder(t *testing.T, f workspaceStoreFixture, id string) store.ConsentEvidence {
	t.Helper()
	pending, err := f.db.PendingImports(context.Background(), 50)
	pairOK(t, err)
	for _, imp := range pending {
		if imp.RemoteID == id {
			return imp.Consent
		}
	}
	t.Fatalf("%s not staged", id)
	return store.ConsentEvidence{}
}

func failOwnerWrites(t *testing.T, f workspaceStoreFixture, on bool) {
	t.Helper()
	stmts := []string{
		`CREATE TRIGGER fail_owner_insert BEFORE INSERT ON consent_owner BEGIN SELECT RAISE(ABORT, 'injected'); END`,
		`CREATE TRIGGER fail_owner_update BEFORE UPDATE ON consent_owner BEGIN SELECT RAISE(ABORT, 'injected'); END`,
		`CREATE TRIGGER fail_owner_delete BEFORE DELETE ON consent_owner BEGIN SELECT RAISE(ABORT, 'injected'); END`,
	}
	if !on {
		stmts = []string{`DROP TRIGGER fail_owner_insert`, `DROP TRIGGER fail_owner_update`, `DROP TRIGGER fail_owner_delete`}
	}
	for _, stmt := range stmts {
		_, err := f.raw.Exec(stmt)
		pairOK(t, err)
	}
}

func TestConsentOwnerSessionGrantAdmitsOnlyForItsLiveOwner(t *testing.T) {
	f := consentFixture(t, "alpha")
	ctx := context.Background()
	pairOK(t, f.db.PeerMessages().SetConsentOwner(ctx, "epoch", 1))
	sessionDecide(t, f, consentHeld(t, f, consentMessage("first", 1, "alpha"), messageAt), 1)
	if arrival(t, f, "second", 2) {
		t.Fatal("a live session grant did not admit the same scope")
	}
	got := stagedUnder(t, f, "second")
	pairEqual(t, got, store.ConsentEvidence{Kind: protocol.ConsentSession, Epoch: "epoch", Gen: 1})

	// (1) a failed End leaves the old row, yet nothing is admitted.
	failOwnerWrites(t, f, true)
	if err := f.db.PeerMessages().EndConsentOwner(ctx, "epoch", 1); err == nil {
		t.Fatal("injected End failure succeeded")
	}
	if !arrival(t, f, "after-failed-end", 3) {
		t.Fatal("a failed End left session admission open")
	}
	// (2) a failed Set after replacement admits nothing either.
	if err := f.db.PeerMessages().SetConsentOwner(ctx, "epoch", 2); err == nil {
		t.Fatal("injected Set failure succeeded")
	}
	if !arrival(t, f, "after-failed-set", 4) {
		t.Fatal("a failed Set left session admission open")
	}
	// (3) a later Set admits only the new owner's grants.
	failOwnerWrites(t, f, false)
	pairOK(t, f.db.PeerMessages().SetConsentOwner(ctx, "epoch", 2))
	v := consentHeldPending(t, f, "old-grant", 5)
	sessionDecide(t, f, v, 2)
	if arrival(t, f, "new-grant", 6) {
		t.Fatal("the new owner's session grant did not admit")
	}
	pairEqual(t, stagedUnder(t, f, "new-grant").Gen, uint64(2))
}

// consentHeldPending is a new arrival that must wait for the operator.
func consentHeldPending(t *testing.T, f workspaceStoreFixture, id string, n uint64) protocol.ConsentView {
	t.Helper()
	return consentHeld(t, f, consentMessage(id, n, "alpha"), messageAt)
}

func TestConsentOwnerNoLiveOwnerNoSessionAdmission(t *testing.T) {
	f := consentFixture(t, "alpha")
	ctx := context.Background()
	pairOK(t, f.db.PeerMessages().SetConsentOwner(ctx, "epoch", 1))
	sessionDecide(t, f, consentHeld(t, f, consentMessage("first", 1, "alpha"), messageAt), 1)
	pairOK(t, f.db.PeerMessages().EndConsentOwner(ctx, "epoch", 1))
	if !arrival(t, f, "no-owner", 2) {
		t.Fatal("a session grant admitted with no live owner")
	}
}

func TestConsentOwnerDenialEndsSessionAndWithholdsQueued(t *testing.T) {
	f := consentFixture(t, "alpha")
	ctx := context.Background()
	pairOK(t, f.db.PeerMessages().SetConsentOwner(ctx, "epoch", 1))
	a := consentHeld(t, f, consentMessage("a", 1, "alpha"), messageAt)
	once := consentDecision(a, protocol.ConsentOnce, messageAt)
	_, err := f.db.DecideConsent(ctx, once)
	pairOK(t, err)
	b := consentHeld(t, f, consentMessage("b", 2, "alpha"), messageAt)
	_, err = f.db.DecideConsent(ctx, consentDecision(b, protocol.ConsentDecline, messageAt))
	pairOK(t, err)
	pending, err := f.db.PendingImports(ctx, 50)
	pairOK(t, err)
	pairEqual(t, len(pending), 0)
	outcomes, err := f.db.PeerMessages().PendingConsentOutcomes(ctx, f.key, 20)
	pairOK(t, err)
	reasons := map[string]string{}
	for _, o := range outcomes {
		reasons[o.ID] = o.State + "/" + o.Reason
	}
	pairEqual(t, reasons["a"], "withheld/"+protocol.ConsentDenied)
	pairEqual(t, reasons["b"], "declined/"+protocol.ConsentDenied)
	answer, err := f.consentAccept(t, 8, consentMessage("c", 3, "alpha"), messageAt)
	pairOK(t, err)
	pairEqual(t, answer.Reason, messaging.ReasonNotShared)
}
