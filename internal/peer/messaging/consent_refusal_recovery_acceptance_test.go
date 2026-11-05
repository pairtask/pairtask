package messaging_test

import (
	"context"
	"errors"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"testing"
	"time"
)

func TestConsentRefusalRecoveryAcceptanceRealSessionsDoNotReplayPoisonOutcome(t *testing.T) {
	p := newConsentConflictPair(t)
	before := consentOrderingSnapshot(t, p.leftPath)
	first := sessionExchange(t, p.left.PeerMessages(), p.right.PeerMessages(), 0)
	sessionSuccess(t, first)
	conflicts, refused := 0, 0
	for _, side := range first {
		conflicts += len(side.result.ConsentConflicts)
		refused += side.result.ConsentOutcomesRefused
		for _, id := range side.result.ConsentConflicts {
			sessionEqual(t, id, p.request)
		}
	}
	sessionEqual(t, conflicts, 1)
	sessionEqual(t, refused, 1)
	after := consentOrderingSnapshot(t, p.leftPath)
	for table, want := range before {
		sessionEqual(t, after[table], want)
	}
	queue := consentRefusalQueue(t, p.right)
	pending, err := queue.PendingConsentOutcomes(context.Background(), sessionContact, 20)
	sessionOK(t, err)
	sessionEqual(t, len(pending), 0)
	consentRefusalDiagnosis(t, p.rightPath, p.request)
	sessionOK(t, p.left.Close())
	sessionOK(t, p.right.Close())
	p.left = sessionDB(t, p.leftPath)
	p.right = sessionDB(t, p.rightPath)
	sessionQueue(t, p.left.PeerMessages(), "ordinary-after-conflict-left")
	sessionQueue(t, p.right.PeerMessages(), "ordinary-after-conflict-right")
	second := sessionExchange(t, p.left.PeerMessages(), p.right.PeerMessages(), 0)
	sessionSuccess(t, second)
	for _, side := range second {
		sessionEqual(t, len(side.result.ConsentConflicts), 0)
		sessionEqual(t, side.result.ConsentOutcomesRefused, 0)
		sessionEqual(t, side.result.ConsentOutcomesSent, 0)
		sessionEqual(t, side.result.Sent, 1)
		sessionEqual(t, side.result.Received, 1)
	}
	consentRefusalDiagnosis(t, p.rightPath, p.request)
}

func TestConsentRefusalRecoveryAcceptanceDuplicateUnknownAndOtherContactDoNothing(t *testing.T) {
	p := newConsentConflictPair(t)
	ctx := context.Background()
	q := consentRefusalQueue(t, p.right)
	refusal := messaging.ConsentOutcomeRefused{ID: p.request, Reason: "terminal_conflict"}
	sessionOK(t, q.ConsentOutcomeRefused(ctx, sessionContact, refusal))
	consentRefusalPending(t, p.right, "still-pending", 2)
	other := consentRefusalOtherContact(t, p.right)
	before := consentOrderingSnapshot(t, p.rightPath)
	for _, r := range []messaging.ConsentOutcomeRefused{refusal, {ID: "unknown-request", Reason: "terminal_conflict"}} {
		sessionOK(t, q.ConsentOutcomeRefused(ctx, sessionContact, r))
	}
	sessionOK(t, q.ConsentOutcomeRefused(ctx, other, messaging.ConsentOutcomeRefused{ID: "still-pending", Reason: "terminal_conflict"}))
	after := consentOrderingSnapshot(t, p.rightPath)
	for table, want := range before {
		sessionEqual(t, after[table], want)
	}
	pending, err := q.PendingConsentOutcomes(ctx, sessionContact, 20)
	sessionOK(t, err)
	sessionEqual(t, len(pending), 1)
	sessionEqual(t, pending[0].ID, "still-pending")
}

func TestConsentRefusalRecoveryAcceptanceSameStateDifferentReasonConflicts(t *testing.T) {
	db, path := sessionFixture(t)
	ctx := context.Background()
	_, err := db.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: sessionContact, ID: "same-state", Text: "body", Now: sessionAt})
	sessionOK(t, err)
	cs := consentOutcomeDurableStore(t, db.PeerMessages())
	sessionOK(t, cs.HeldBy(ctx, sessionContact, messaging.Held{ID: "same-state", HeldID: "hold"}, sessionAt))
	first := messaging.ConsentOutcome{ID: "same-state", State: "withheld", Reason: "session_ended"}
	sessionOK(t, cs.AcceptConsentOutcome(ctx, sessionContact, first, sessionAt))
	before := consentOrderingSnapshot(t, path)
	changed := first
	changed.Reason = "revoked"
	err = cs.AcceptConsentOutcome(ctx, sessionContact, changed, sessionAt.Add(time.Hour))
	if !errors.Is(err, messaging.ErrConsentTerminalConflict) {
		t.Fatalf("same-state changed-reason contradiction wasn't refused: %v", err)
	}
	after := consentOrderingSnapshot(t, path)
	for table, want := range before {
		sessionEqual(t, after[table], want)
	}
}
