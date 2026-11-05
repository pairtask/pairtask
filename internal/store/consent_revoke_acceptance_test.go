package store_test

import (
	"context"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
	"testing"
	"time"
)

func TestConsentRevokeAcceptanceUnshareIsTargetScopedAndAtomicForGroup(t *testing.T) {
	for _, kind := range []string{protocol.ConsentOnce, protocol.ConsentSession} {
		t.Run(kind, func(t *testing.T) {
			f := consentFixture(t, "X", "Y")
			ctx := context.Background()
			for i, targets := range [][]string{{"X", "Y"}, {"Y"}} {
				id := []string{"group-X-Y", "only-Y"}[i]
				v := consentHeld(t, f, consentMessage(id, uint64(i+1), targets...), messageAt)
				d := consentDecision(v, kind, messageAt)
				d.AuditID = consentAudit(t, f, "consent_decide")
				_, err := f.db.DecideConsent(ctx, d)
				pairOK(t, err)
			}
			_, err := f.db.PeerMessages().Unshare(ctx, f.key, []string{"X"}, messageAt.Add(time.Minute))
			pairOK(t, err)
			group, err := f.db.ImportRemote(ctx, store.RemoteImport{Contact: f.key, RemoteID: "group-X-Y"}, 1, messageAt.Add(time.Minute))
			pairOK(t, err)
			pairEqual(t, group.State, store.ImportWithheld)
			solo, err := f.db.ImportRemote(ctx, store.RemoteImport{Contact: f.key, RemoteID: "only-Y"}, 2, messageAt.Add(time.Minute))
			pairOK(t, err)
			pairEqual(t, solo.State, store.ImportImported)
			x, err := f.db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "X", SessionID: "new-X"}, 24)
			pairOK(t, err)
			pairEqual(t, len(x), 0)
			y, err := f.db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "Y", SessionID: "new-Y"}, 24)
			pairOK(t, err)
			pairEqual(t, len(y), 1)
			pairEqual(t, y[0].MessageID, solo.Msg.MessageID)
			a, err := f.consentAccept(t, 8, consentMessage("new-X-after-reregister", 3, "X"), messageAt.Add(2*time.Minute))
			pairOK(t, err)
			if a.Held || a.Stored {
				t.Fatalf("re-registering X erased explicit denial: %+v", a)
			}
			outcomes, err := consentQueuedOutcomes(t, f).PendingConsentOutcomes(ctx, f.key, 20)
			pairOK(t, err)
			pairEqual(t, len(outcomes), 2)
			for _, o := range outcomes {
				if o.ID == "group-X-Y" {
					pairEqual(t, o.State, "withheld")
					pairEqual(t, o.Reason, "revoked")
				} else {
					pairEqual(t, o.ID, "only-Y")
					pairEqual(t, o.State, "allowed")
				}
			}
		})
	}
}
