package messaging_test

import (
	"context"
	"errors"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"reflect"
	"testing"
)

func TestConsentRefusalCodecAcceptanceExactIndependentShape(t *testing.T) {
	raw := consentRawFrame(26, "original-request", "terminal_conflict")
	r, err := messaging.DecodeConsentOutcomeRefused(raw)
	sessionOK(t, err)
	sessionEqual(t, r.ID, "original-request")
	sessionEqual(t, r.Reason, messaging.ConsentRefusedTerminalConflict)
	encoded, err := messaging.EncodeConsentOutcomeRefused(r)
	sessionOK(t, err)
	sessionEqual(t, reflect.DeepEqual(encoded, raw), true)
	kind, err := messaging.Kind(raw)
	sessionOK(t, err)
	sessionEqual(t, kind, byte(26))
}

func TestConsentRefusalCodecAcceptanceMalformedOrEmptyRefusalRejected(t *testing.T) {
	for _, raw := range [][]byte{consentRawFrame(26, "", "terminal_conflict"), consentRawFrame(26, "request", ""), consentRawFrame(26, "bad id", "terminal_conflict"), consentRawFrame(26, "request", "terminal_conflict", "extra"), {1, 26, 0, 9, 'x'}} {
		if _, err := messaging.DecodeConsentOutcomeRefused(raw); !errors.Is(err, messaging.ErrProtocol) {
			t.Errorf("malformed consent refusal accepted: %x err=%v", raw, err)
		}
	}
}

func TestConsentRefusalCodecAcceptanceV7UnexpectedRefusalIsProtocolError(t *testing.T) {
	p := newConsentOutcomeProbe(t)
	// Refusal is only an answer in v8's consent phase; under v7 it can
	// never be accepted as a reply in the existing outcome phase.
	input := append(outcomePhasePrefix(t, 7), consentRawFrame(26, "request", "terminal_conflict"))
	frames := &scriptFrames{input: input}
	_, version, err := sessionSide(frames, p).OpenDial(context.Background())
	sessionEqual(t, version, byte(7))
	sessionErrorIs(t, err, messaging.ErrProtocol)
}
