package store_test

import (
	"context"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
	"testing"
	"time"
)

func consentStageImport(t *testing.T, f workspaceStoreFixture, m messaging.Message, kind string) store.RemoteImport {
	t.Helper()
	v := consentHeld(t, f, m, messageAt)
	d := consentDecision(v, kind, messageAt)
	d.AuditID = consentAudit(t, f, "consent_decide")
	_, err := f.db.DecideConsent(context.Background(), d)
	pairOK(t, err)
	pending, err := f.db.PendingImports(context.Background(), 20)
	pairOK(t, err)
	for _, imp := range pending {
		if imp.RemoteID == m.ID {
			return imp
		}
	}
	t.Fatal("allowed request not staged")
	return store.RemoteImport{}
}

func TestConsentReasonAcceptanceExactWithheldReasonSurvivesImportPruneAndRestart(t *testing.T) {
	for _, reason := range []string{"session_ended", "paused", "revoked"} {
		t.Run(reason, func(t *testing.T) {
			f := consentFixture(t, "alpha")
			imp := consentStageImport(t, f, consentMessage("withheld-original", 1, "alpha"), protocol.ConsentSession)
			out, err := f.db.WithholdConsent(context.Background(), imp, reason, messageAt.Add(time.Minute))
			pairOK(t, err)
			pairEqual(t, out.State, store.ImportWithheld)
			_, err = f.raw.Exec(`DELETE FROM remote_imports WHERE contact=? AND remote_id=?`, f.key, imp.RemoteID)
			pairOK(t, err)
			f = consentStoreReopen(t, f)
			outcomes, err := consentQueuedOutcomes(t, f).PendingConsentOutcomes(context.Background(), f.key, 20)
			pairOK(t, err)
			pairEqual(t, len(outcomes), 1)
			pairEqual(t, outcomes[0].ID, imp.RemoteID)
			pairEqual(t, outcomes[0].State, "withheld")
			pairEqual(t, outcomes[0].Reason, reason)
		})
	}
}

func TestConsentReasonAcceptanceReasonAndWithholdingCommitAtomically(t *testing.T) {
	f := consentFixture(t, "alpha")
	imp := consentStageImport(t, f, consentMessage("atomic-reason", 1, "alpha"), protocol.ConsentSession)
	_, err := f.raw.Exec(`CREATE TRIGGER fail_reason BEFORE UPDATE ON consent_requests BEGIN SELECT RAISE(ABORT,'injected reason failure'); END`)
	pairOK(t, err)
	before := consentSnapshot(t, f)
	if _, err = f.db.WithholdConsent(context.Background(), imp, "session_ended", messageAt.Add(time.Minute)); err == nil {
		t.Fatal("injected reason failure committed partial withholding")
	}
	consentUnchanged(t, f, before)
	_, err = f.raw.Exec(`DROP TRIGGER fail_reason`)
	pairOK(t, err)
	_, err = f.db.WithholdConsent(context.Background(), imp, "session_ended", messageAt.Add(time.Minute))
	pairOK(t, err)
	outcomes, err := consentQueuedOutcomes(t, f).PendingConsentOutcomes(context.Background(), f.key, 20)
	pairOK(t, err)
	pairEqual(t, len(outcomes), 1)
	pairEqual(t, outcomes[0].Reason, "session_ended")
}
