package store_test

import (
	"context"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"testing"
	"time"
)

func consentSettleStandalone(t *testing.T, f workspaceStoreFixture, m messaging.Message, state string) {
	t.Helper()
	v := consentHeld(t, f, m, messageAt)
	ctx := context.Background()
	if state == "expired" {
		n, err := f.db.ExpireConsent(ctx, v.ExpiresAt)
		pairOK(t, err)
		pairEqual(t, n, 1)
	} else {
		_, err := f.db.DecideConsent(ctx, consentDecision(v, protocol.ConsentDecline, messageAt))
		pairOK(t, err)
	}
	q := consentQueuedOutcomes(t, f)
	out, err := q.PendingConsentOutcomes(ctx, f.key, 20)
	pairOK(t, err)
	pairEqual(t, len(out), 1)
	pairOK(t, q.ConsentOutcomeAnswered(ctx, f.key, messaging.ConsentOutcomeStored{ID: m.ID}))
	reviews, err := f.db.PeerMessages().PendingOutcomes(ctx, f.key, 20)
	pairOK(t, err)
	for _, o := range reviews {
		pairOK(t, f.db.PeerMessages().OutcomeAnswered(ctx, f.key, messaging.OutcomeReply{AskID: o.AskID, Seq: o.Seq}))
	}
}
func consentAssertStandaloneCollected(t *testing.T, f workspaceStoreFixture, m messaging.Message) {
	t.Helper()
	now := messageAt.Add(messaging.QueuedExpiry + messaging.TombstoneAge + time.Hour)
	pairOK(t, f.db.PeerMessages().Collect(context.Background(), now))
	f = consentStoreReopen(t, f)
	for _, table := range []string{"consent_requests", "consent_rate"} {
		var n int
		pairOK(t, f.raw.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE contact=?", f.key).Scan(&n))
		pairEqual(t, n, 0)
	}
	a, err := f.consentAccept(t, 8, m, now)
	pairOK(t, err)
	pairEqual(t, a.Held, false)
	pairEqual(t, a.Reason, messaging.ReasonTooOld)
}
func TestConsentStandaloneCollectionAcceptanceSettledOnlyContactIsNotForgotten(t *testing.T) {
	for _, state := range []string{"declined", "expired"} {
		for _, review := range []bool{false, true} {
			name := state + "-message"
			if review {
				name = state + "-review"
			}
			t.Run(name, func(t *testing.T) {
				f, m := consentRetentionRequest(t, review)
				m.N = 1
				consentSettleStandalone(t, f, m, state)
				for _, table := range []string{"peer_messages", "remote_imports", "review_asks_out"} {
					var n int
					pairOK(t, f.raw.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE contact=?", f.key).Scan(&n))
					pairEqual(t, n, 0)
				}
				consentAssertStandaloneCollected(t, f, m)
			})
		}
	}
}
