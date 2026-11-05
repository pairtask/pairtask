package messaging_test

import (
	"errors"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"reflect"
	"testing"
)

func TestConsentCodecAcceptanceIndependentPeerFrames(t *testing.T) {
	held, err := messaging.DecodeHeld(consentRawFrame(23, "request", "hold"))
	sessionOK(t, err)
	sessionEqual(t, held, messaging.Held{ID: "request", HeldID: "hold"})
	encoded, err := messaging.EncodeHeld(held)
	sessionOK(t, err)
	sessionEqual(t, reflect.DeepEqual(encoded, consentRawFrame(23, "request", "hold")), true)
	for _, state := range []string{"allowed", "declined", "expired", "withheld"} {
		t.Run(state, func(t *testing.T) {
			reason := ""
			if state == "withheld" {
				reason = "session_ended"
			}
			o, err := messaging.DecodeConsentOutcome(consentRawFrame(24, "request", state, reason))
			sessionOK(t, err)
			sessionEqual(t, o.ID, "request")
			sessionEqual(t, o.State, state)
			frame, err := messaging.EncodeConsentOutcome(o)
			sessionOK(t, err)
			sessionEqual(t, reflect.DeepEqual(frame, consentRawFrame(24, "request", state, reason)), true)
		})
	}
	ack, err := messaging.DecodeConsentOutcomeStored(consentRawFrame(25, "request"))
	sessionOK(t, err)
	sessionEqual(t, ack.ID, "request")
	encoded, err = messaging.EncodeConsentOutcomeStored(ack)
	sessionOK(t, err)
	sessionEqual(t, reflect.DeepEqual(encoded, consentRawFrame(25, "request")), true)
}

func TestConsentCodecAcceptanceMalformedFramesCannotSupplyAuthority(t *testing.T) {
	for _, frame := range [][]byte{consentRawFrame(23, "", "hold"), consentRawFrame(23, "request", ""), consentRawFrame(23, "request", "hold", "extra"), {1, 23, 0, 10, 'x'}} {
		if _, err := messaging.DecodeHeld(frame); !errors.Is(err, messaging.ErrProtocol) {
			t.Errorf("malformed Held accepted: %x err=%v", frame, err)
		}
	}
	for _, frame := range [][]byte{consentRawFrame(24, "", "allowed", ""), consentRawFrame(24, "request", "always", ""), consentRawFrame(24, "request", "pending", ""), consentRawFrame(24, "request", "allowed", "", "extra")} {
		if _, err := messaging.DecodeConsentOutcome(frame); !errors.Is(err, messaging.ErrProtocol) {
			t.Errorf("malformed outcome accepted: %x err=%v", frame, err)
		}
	}
}
