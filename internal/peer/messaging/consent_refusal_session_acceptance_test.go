package messaging_test

import (
	"context"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"testing"
)

func TestConsentRefusalSessionAcceptanceMismatchDoesNotSettleOrKillRound(t *testing.T) {
	p := newConsentOutcomeProbe(t)
	frames := &scriptFrames{input: append(consentOutcomePrefix(t, 8), consentRawFrame(26, "unrelated-request", "terminal_conflict"), routingSessionEnd(t))}
	result, version, err := sessionSide(frames, p).OpenDial(context.Background())
	sessionOK(t, err)
	sessionEqual(t, version, byte(8))
	sessionEqual(t, len(p.pendingConsent), 1)
	sessionEqual(t, p.pendingConsent[0].ID, "request")
	sessionEqual(t, result.ConsentOutcomesRefused, 0)
}

func TestConsentRefusalSessionAcceptanceMatchingRefusalSettlesAndContinues(t *testing.T) {
	p := newConsentOutcomeProbe(t)
	frames := &scriptFrames{input: append(consentOutcomePrefix(t, 8), consentRawFrame(26, "request", "terminal_conflict"), routingSessionEnd(t))}
	result, version, err := sessionSide(frames, p).OpenDial(context.Background())
	sessionOK(t, err)
	sessionEqual(t, version, byte(8))
	sessionEqual(t, result.ConsentOutcomesRefused, 1)
	sessionEqual(t, result.Moved(), true)
	sessionEqual(t, len(p.pendingConsent), 0)
	sessionEqual(t, len(p.consentRefusals), 1)
	sessionEqual(t, p.consentRefusals[0], messaging.ConsentOutcomeRefused{ID: "request", Reason: "terminal_conflict"})
}

func TestConsentRefusalSessionAcceptanceConflictContinuesToNextOutcome(t *testing.T) {
	db, _ := sessionFixture(t)
	ctx := context.Background()
	cs := consentOutcomeDurableStore(t, db.PeerMessages())
	for _, id := range []string{"conflict", "valid"} {
		_, err := db.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: sessionContact, ID: id, Text: "body", Now: sessionAt})
		sessionOK(t, err)
		sessionOK(t, cs.HeldBy(ctx, sessionContact, messaging.Held{ID: id, HeldID: "hold-" + id}, sessionAt))
	}
	sessionOK(t, cs.AcceptConsentOutcome(ctx, sessionContact, messaging.ConsentOutcome{ID: "conflict", State: "allowed"}, sessionAt))
	frames := &scriptFrames{input: append(consentOutcomePrefix(t, 8), consentRawFrame(24, "conflict", "declined", "denied"), consentRawFrame(24, "valid", "declined", "denied"), routingSessionEnd(t))}
	result, _, err := sessionSide(frames, db.PeerMessages()).OpenDial(ctx)
	sessionOK(t, err)
	sessionEqual(t, len(result.ConsentConflicts), 1)
	sessionEqual(t, result.ConsentConflicts[0], "conflict")
	sessionEqual(t, result.ConsentOutcomesStored, 1)
	refused, stored := 0, 0
	for _, raw := range frames.sent {
		kind, err := messaging.Kind(raw)
		sessionOK(t, err)
		switch kind {
		case 26:
			r, err := messaging.DecodeConsentOutcomeRefused(raw)
			sessionOK(t, err)
			sessionEqual(t, r.ID, "conflict")
			sessionEqual(t, r.Reason, "terminal_conflict")
			refused++
		case 25:
			a, err := messaging.DecodeConsentOutcomeStored(raw)
			sessionOK(t, err)
			if a.ID == "conflict" {
				t.Fatal("contradictory terminal outcome falsely acknowledged as stored")
			}
			sessionEqual(t, a.ID, "valid")
			stored++
		}
	}
	sessionEqual(t, refused, 1)
	sessionEqual(t, stored, 1)
}
