package store_test

import (
	"context"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"testing"
)

func TestConsentTombstoneCapAcceptanceSharesInboundBoundAndNeverEvictsDedup(t *testing.T) {
	for _, review := range []bool{false, true} {
		name := "message"
		if review {
			name = "review"
		}
		t.Run(name, func(t *testing.T) {
			f, m := consentRetentionRequest(t, review)
			ctx := context.Background()
			anchor := messageWire("cap-anchor", 1)
			_, err := f.consentAccept(t, 8, anchor, messageAt)
			pairOK(t, err)
			_, err = f.db.PeerMessages().MarkRead(ctx, f.key, []string{anchor.ID}, messageAt)
			pairOK(t, err)
			messageSeedSettled(t, f.raw, f.key, messaging.Tombstones-1)
			m.N = messaging.Tombstones
			v := consentHeld(t, f, m, messageAt)
			d := consentDecision(v, protocol.ConsentDecline, messageAt)
			_, err = f.db.DecideConsent(ctx, d)
			pairOK(t, err)
			pairOK(t, consentQueuedOutcomes(t, f).ConsentOutcomeAnswered(ctx, f.key, messaging.ConsentOutcomeStored{ID: m.ID}))
			agent, op := "alpha", protocol.ConsentMessage
			if review {
				agent, op = "reviewer", protocol.ConsentReview
			}
			pairOK(t, f.db.ResetConsentScope(ctx, f.key, agent, op, 0, messageAt))
			if review {
				pairOK(t, f.db.ResetConsentScope(ctx, f.key, agent, protocol.ConsentPublish, 0, messageAt))
			}
			rate := reviewIDTable(t, f, "SELECT * FROM consent_rate ORDER BY contact")
			next := m
			next.ID = "past-shared-tombstone-cap"
			next.N++
			a, err := f.consentAccept(t, 8, next, messageAt)
			pairOK(t, err)
			pairEqual(t, a.Held, false)
			pairEqual(t, a.Reason, messaging.ReasonInboxFull)
			pairEqual(t, reviewIDTable(t, f, "SELECT * FROM consent_rate ORDER BY contact"), rate)
			ordinary := messageWire("ordinary-past-shared-tombstone-cap", m.N+1)
			a, err = f.consentAccept(t, 8, ordinary, messageAt)
			pairOK(t, err)
			pairEqual(t, a.Stored, false)
			pairEqual(t, a.Reason, messaging.ReasonInboxFull)
			var count int
			pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM consent_requests WHERE id=? AND digest=?`, v.ID, v.Digest).Scan(&count))
			pairEqual(t, count, 1)
			a, err = f.consentAccept(t, 8, m, messageAt)
			pairOK(t, err)
			pairEqual(t, a.Held, false)
		})
	}
}
