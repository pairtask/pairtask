package messaging_test

import (
	"context"
	"errors"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"io"
	"testing"
	"time"
)

type consentOutcomeProbe struct {
	*outcomeProbe
	pendingConsent  []messaging.ConsentOutcome
	consentReads    int
	consentReplies  []messaging.ConsentOutcomeStored
	receivedConsent []messaging.ConsentOutcome
	consentRefusals []messaging.ConsentOutcomeRefused
}

func (p *consentOutcomeProbe) PendingConsentOutcomes(context.Context, string, int) ([]messaging.ConsentOutcome, error) {
	p.consentReads++
	return p.pendingConsent, nil
}
func (p *consentOutcomeProbe) ConsentOutcomeAnswered(_ context.Context, _ string, a messaging.ConsentOutcomeStored) error {
	p.consentReplies = append(p.consentReplies, a)
	p.pendingConsent = nil
	return nil
}
func (p *consentOutcomeProbe) HeldBy(context.Context, string, messaging.Held, time.Time) error {
	return nil
}
func (p *consentOutcomeProbe) AcceptConsentOutcome(_ context.Context, _ string, o messaging.ConsentOutcome, _ time.Time) error {
	p.receivedConsent = append(p.receivedConsent, o)
	return nil
}
func newConsentOutcomeProbe(t *testing.T) *consentOutcomeProbe {
	db, _ := sessionFixture(t)
	return &consentOutcomeProbe{outcomeProbe: &outcomeProbe{routingSessionProbe: &routingSessionProbe{Store: db.PeerMessages()}}, pendingConsent: []messaging.ConsentOutcome{{ID: "request", State: "declined", Reason: "denied"}}}
}
func consentOutcomePrefix(t *testing.T, version byte) [][]byte {
	return append(outcomePhasePrefix(t, version), routingSessionEnd(t))
}

func TestConsentOutcomeAcceptanceV7NeverReadsOrSendsPendingOutcomes(t *testing.T) {
	p := newConsentOutcomeProbe(t)
	frames := &scriptFrames{input: consentOutcomePrefix(t, 7)}
	_, version, err := sessionSide(frames, p).OpenDial(context.Background())
	sessionOK(t, err)
	sessionEqual(t, version, byte(7))
	sessionEqual(t, p.consentReads, 0)
	sessionEqual(t, len(p.pendingConsent), 1)
	for _, b := range frames.sent {
		k, err := messaging.Kind(b)
		sessionOK(t, err)
		if k >= 23 && k <= 25 {
			t.Fatalf("old peer received new frame %d", k)
		}
	}
}

func TestConsentOutcomeAcceptanceWrongAcknowledgmentCannotSettle(t *testing.T) {
	p := newConsentOutcomeProbe(t)
	frames := &scriptFrames{input: append(consentOutcomePrefix(t, 8), consentRawFrame(25, "different-request"))}
	_, _, err := sessionSide(frames, p).OpenDial(context.Background())
	sessionErrorIs(t, err, messaging.ErrProtocol)
	sessionEqual(t, len(p.consentReplies), 0)
	sessionEqual(t, len(p.pendingConsent), 1)
}

func TestConsentOutcomeAcceptanceAcknowledgmentSettlesOnlySentRequest(t *testing.T) {
	p := newConsentOutcomeProbe(t)
	frames := &scriptFrames{input: append(consentOutcomePrefix(t, 8), consentRawFrame(25, "request"), routingSessionEnd(t))}
	result, version, err := sessionSide(frames, p).OpenDial(context.Background())
	sessionOK(t, err)
	sessionEqual(t, version, byte(8))
	sessionEqual(t, result.Moved(), true)
	sessionEqual(t, len(p.consentReplies), 1)
	sessionEqual(t, p.consentReplies[0].ID, "request")
	sessionEqual(t, len(p.pendingConsent), 0)
}

func TestConsentOutcomeAcceptanceStoreBeforeAnswerAndSignalDurableProgress(t *testing.T) {
	p := newConsentOutcomeProbe(t)
	p.pendingConsent = nil
	frames := &scriptFrames{input: append(consentOutcomePrefix(t, 8), consentRawFrame(24, "request", "declined", "denied")), failKind: 25}
	result, _, err := sessionSide(frames, p).OpenDial(context.Background())
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("lost outcome answer not injected: %v", err)
	}
	sessionEqual(t, len(p.receivedConsent), 1)
	sessionEqual(t, p.receivedConsent[0].ID, "request")
	sessionEqual(t, result.Moved(), true)
}

func (p *consentOutcomeProbe) ConsentOutcomeRefused(_ context.Context, _ string, r messaging.ConsentOutcomeRefused) error {
	p.consentRefusals = append(p.consentRefusals, r)
	for _, o := range p.pendingConsent {
		if o.ID == r.ID {
			p.pendingConsent = nil
			break
		}
	}
	return nil
}
