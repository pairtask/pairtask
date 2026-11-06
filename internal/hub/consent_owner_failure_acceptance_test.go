package hub

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestConsentOwnerFailureAcceptanceCloseWithholdsQueuedDispatchDespiteDatabaseFailure(t *testing.T) {
	for _, isReview := range []bool{false, true} {
		name := "message"
		if isReview {
			name = "review"
		}
		t.Run(name, func(t *testing.T) {
			h, recipient := consentDispatchFixture(t, protocol.ConsentSession, "live", isReview)
			ctx := context.Background()
			pairOwner, ok := any(h.store.PeerMessages()).(interface {
				SetConsentOwner(context.Context, string, uint64) error
			})
			if !ok {
				t.Fatal("live consent owner API is missing")
			}
			messageWorkerOK(t, pairOwner.SetConsentOwner(ctx, h.epoch, 7))
			raw, err := sql.Open("sqlite", strings.TrimSuffix(h.store.PeerDir(), ".peer"))
			messageWorkerOK(t, err)
			defer raw.Close()
			_, err = raw.Exec(`CREATE TRIGGER acceptance_fail_owner_delete BEFORE DELETE ON consent_owner BEGIN SELECT RAISE(ABORT, 'owner close injected failure'); END`)
			messageWorkerOK(t, err)
			// Use the real close handler before Run's initial import drain. This
			// deterministically closes between approval and the dispatch boundary.
			h.closeSession(ctx, h.owner)
			if h.owner != nil {
				t.Fatal("failed database close retained live hub owner")
			}
			var oldRows int
			messageWorkerOK(t, raw.QueryRow(`SELECT COUNT(*) FROM consent_owner WHERE epoch = ? AND gen = 7`, h.epoch).Scan(&oldRows))
			messageSyncEqual(t, oldRows, 1)
			consentDispatchPass(t, h, recipient)
			remoteHubEmpty(t, recipient)
			rows, err := h.store.ClaimPendingFor(ctx, protocol.Recipient{AgentID: recipient.ID, SessionID: "failure-inspection"}, 24)
			messageWorkerOK(t, err)
			messageSyncEqual(t, len(rows), 0)
			pending, err := h.store.PendingImports(ctx, 20)
			messageWorkerOK(t, err)
			messageSyncEqual(t, len(pending), 0)
			reviews, total, err := h.store.Reviews(ctx, recipient.ID, 20)
			messageWorkerOK(t, err)
			messageSyncEqual(t, total, 0)
			messageSyncEqual(t, len(reviews), 0)
		})
	}
}
