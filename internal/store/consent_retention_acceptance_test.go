package store_test

import (
	"context"
	"database/sql"
	"errors"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
	"testing"
	"time"
)

func consentRetentionRequest(t *testing.T, review bool) (workspaceStoreFixture, messaging.Message) {
	t.Helper()
	if review {
		f := consentReviewFixture(t)
		m := remoteAsk("retained-review")
		m.N = 2
		return f, m
	}
	return consentFixture(t, "alpha"), consentMessage("retained-message", 2, "alpha")
}
func consentRetainedPayload(t *testing.T, f workspaceStoreFixture, id string, keep bool, review bool) {
	t.Helper()
	var text, ask, targets sql.NullString
	pairOK(t, f.raw.QueryRow(`SELECT text,review_ask,targets FROM consent_requests WHERE id=?`, id).Scan(&text, &ask, &targets))
	pairEqual(t, text.Valid, keep)
	pairEqual(t, targets.Valid, keep)
	pairEqual(t, ask.Valid, keep && review)
}

func TestConsentRetentionAcceptanceUnansweredOutcomeAndPayloadSurviveCompaction(t *testing.T) {
	for _, review := range []bool{false, true} {
		name := "message"
		if review {
			name = "review"
		}
		t.Run(name, func(t *testing.T) {
			f, m := consentRetentionRequest(t, review)
			v := consentHeld(t, f, m, messageAt)
			d := consentDecision(v, protocol.ConsentDecline, messageAt)
			_, err := f.db.DecideConsent(context.Background(), d)
			pairOK(t, err)
			now := messageAt.Add(messaging.TombstoneAge + time.Hour)
			pairOK(t, f.db.PeerMessages().Collect(context.Background(), now))
			f = consentStoreReopen(t, f)
			consentRetainedPayload(t, f, v.ID, true, review)
			outcomes, err := consentQueuedOutcomes(t, f).PendingConsentOutcomes(context.Background(), f.key, 20)
			pairOK(t, err)
			pairEqual(t, len(outcomes), 1)
			pairEqual(t, outcomes[0].ID, m.ID)
			pairEqual(t, outcomes[0].State, "declined")
			a, err := consentAccept(t, f.db, f.key, 8, m, now)
			pairOK(t, err)
			pairEqual(t, a.Held, false)
		})
	}
}

func TestConsentRetentionAcceptancePayloadThenTombstonePruneOnlyBehindSafeFloor(t *testing.T) {
	for _, review := range []bool{false, true} {
		name := "message"
		if review {
			name = "review"
		}
		t.Run(name, func(t *testing.T) {
			f, m := consentRetentionRequest(t, review)
			ctx := context.Background()
			anchor := messageWire("unread-floor-anchor", 1)
			a, err := consentAccept(t, f.db, f.key, 8, anchor, messageAt)
			pairOK(t, err)
			pairEqual(t, a.Stored, true)
			v := consentHeld(t, f, m, messageAt)
			d := consentDecision(v, protocol.ConsentDecline, messageAt)
			_, err = f.db.DecideConsent(ctx, d)
			pairOK(t, err)
			pairOK(t, consentQueuedOutcomes(t, f).ConsentOutcomeAnswered(ctx, f.key, messaging.ConsentOutcomeStored{ID: m.ID}))
			now := messageAt.Add(messaging.TombstoneAge + time.Hour)
			pairOK(t, f.db.PeerMessages().Collect(ctx, now))
			f = consentStoreReopen(t, f)
			consentRetainedPayload(t, f, v.ID, false, review)
			var digest string
			var n uint64
			pairOK(t, f.raw.QueryRow(`SELECT digest,n FROM consent_requests WHERE id=?`, v.ID).Scan(&digest, &n))
			pairEqual(t, digest, v.Digest)
			pairEqual(t, n, m.N)
			a, err = consentAccept(t, f.db, f.key, 8, m, now)
			pairOK(t, err)
			pairEqual(t, a.Held, false)
			changed := m
			changed.Text = "replacement after compaction"
			a, err = consentAccept(t, f.db, f.key, 8, changed, now)
			if !errors.Is(err, store.ErrConsentConflict) && !errors.Is(err, messaging.ErrConflict) && a.Reason != messaging.ReasonConflict {
				t.Fatalf("compacted immutable payload no longer conflicts: %+v", a)
			}
			_, err = f.db.PeerMessages().MarkRead(ctx, f.key, []string{anchor.ID}, now)
			pairOK(t, err)
			pairOK(t, f.db.PeerMessages().ReadConfirmed(ctx, f.key, anchor.ID))
			_, _, err = f.db.PeerMessages().Ack(ctx, f.key, store.AckSelector{IDs: []string{anchor.ID}}, now)
			pairOK(t, err)
			pairOK(t, f.db.PeerMessages().Collect(ctx, now))
			f = consentStoreReopen(t, f)
			var count int
			pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM consent_requests WHERE id=?`, v.ID).Scan(&count))
			pairEqual(t, count, 0)
			a, err = consentAccept(t, f.db, f.key, 8, m, now)
			pairOK(t, err)
			pairEqual(t, a.Held, false)
			pairEqual(t, a.Reason, messaging.ReasonTooOld)
		})
	}
}
