package messaging

import (
	"errors"
	"fmt"
)

// ConsentPeerVersion is the first session version that may answer Held and
// later carry a ConsentOutcome (#293); older peers get today's refusal.
const ConsentPeerVersion = 8

// Version 8 only.
const (
	FrameHeld                 byte = 23 // the receiver holds a request for its operator's decision
	FrameConsentOutcome       byte = 24 // what became of a held request, back to its requester
	FrameConsentOutcomeStored byte = 25 // the requester's answer to a ConsentOutcome
	// FrameConsentOutcomeRefused: the requester's answer to an outcome that
	// contradicts the one it already holds for that request.
	FrameConsentOutcomeRefused byte = 26
)

// ConsentRefusedTerminalConflict: the request already ended otherwise.
const ConsentRefusedTerminalConflict = "terminal_conflict"

// ErrConsentTerminalConflict is an outcome contradicting the terminal one
// already held: refused, never last-write-wins, and the session goes on.
var ErrConsentTerminalConflict = errors.New("messaging: consent outcome contradicts the one held")

// ConsentOutcomeRefused answers an outcome the requester will not take.
type ConsentOutcomeRefused struct {
	ID     string
	Reason string
}

// Held answers a request the receiver holds durably for its operator: the
// requester treats it as stored, and hears the outcome later.
type Held struct {
	ID     string // the requester's message ID
	HeldID string // the receiver's hold
}

// ConsentOutcome tells the requester what became of a held request,
// correlated by the original message ID.
type ConsentOutcome struct {
	ID     string // the requester's message ID
	State  string // allowed, declined, expired, withheld
	Reason string
}

// ConsentOutcomeStored answers a ConsentOutcome the requester holds.
type ConsentOutcomeStored struct{ ID string }

var (
	heldShape                 = shape{FrameHeld, 2}
	consentOutcomeShape       = shape{FrameConsentOutcome, 3}
	consentOutcomeStoredShape = shape{FrameConsentOutcomeStored, 1}
	consentRefusedShape       = shape{FrameConsentOutcomeRefused, 2}
)

// consentStates are the outcomes a held request can end in.
var consentStates = map[string]bool{"allowed": true, "declined": true, "expired": true, "withheld": true}

func EncodeHeld(h Held) ([]byte, error) {
	if !ValidID(h.ID) || !ValidID(h.HeldID) {
		return nil, fmt.Errorf("%w: held needs both IDs", ErrProtocol)
	}
	return encode(FrameHeld, []byte(h.ID), []byte(h.HeldID))
}

func DecodeHeld(frame []byte) (Held, error) {
	f, err := decode(frame, heldShape)
	if err != nil {
		return Held{}, err
	}
	h := Held{ID: string(f[0]), HeldID: string(f[1])}
	if !ValidID(h.ID) || !ValidID(h.HeldID) {
		return Held{}, fmt.Errorf("%w: held needs both IDs", ErrProtocol)
	}
	return h, nil
}

func EncodeConsentOutcome(o ConsentOutcome) ([]byte, error) {
	if !validOutcome(o) {
		return nil, fmt.Errorf("%w: consent outcome", ErrProtocol)
	}
	return encode(FrameConsentOutcome, []byte(o.ID), []byte(o.State), []byte(o.Reason))
}

func DecodeConsentOutcome(frame []byte) (ConsentOutcome, error) {
	f, err := decode(frame, consentOutcomeShape)
	if err != nil {
		return ConsentOutcome{}, err
	}
	o := ConsentOutcome{ID: string(f[0]), State: string(f[1]), Reason: string(f[2])}
	if !validOutcome(o) {
		return ConsentOutcome{}, fmt.Errorf("%w: consent outcome", ErrProtocol)
	}
	return o, nil
}

// validOutcome: a request ID and one of the outcomes a hold can end in.
func validOutcome(o ConsentOutcome) bool {
	return ValidID(o.ID) && consentStates[o.State]
}

func EncodeConsentOutcomeStored(s ConsentOutcomeStored) ([]byte, error) {
	if !ValidID(s.ID) {
		return nil, fmt.Errorf("%w: consent outcome answer needs its ID", ErrProtocol)
	}
	return encode(FrameConsentOutcomeStored, []byte(s.ID))
}

func DecodeConsentOutcomeStored(frame []byte) (ConsentOutcomeStored, error) {
	id, err := decodeID(frame, consentOutcomeStoredShape)
	if err == nil && !ValidID(id) {
		err = fmt.Errorf("%w: consent outcome answer needs its ID", ErrProtocol)
	}
	return ConsentOutcomeStored{ID: id}, err
}

func EncodeConsentOutcomeRefused(r ConsentOutcomeRefused) ([]byte, error) {
	if !validConsentRefusal(r) {
		return nil, fmt.Errorf("%w: consent outcome refusal", ErrProtocol)
	}
	return encode(FrameConsentOutcomeRefused, []byte(r.ID), []byte(r.Reason))
}

func DecodeConsentOutcomeRefused(frame []byte) (ConsentOutcomeRefused, error) {
	f, err := decode(frame, consentRefusedShape)
	if err != nil {
		return ConsentOutcomeRefused{}, err
	}
	r := ConsentOutcomeRefused{ID: string(f[0]), Reason: string(f[1])}
	if !validConsentRefusal(r) {
		return ConsentOutcomeRefused{}, fmt.Errorf("%w: consent outcome refusal", ErrProtocol)
	}
	return r, nil
}

// validConsentRefusal: a request ID and a reason.
func validConsentRefusal(r ConsentOutcomeRefused) bool { return ValidID(r.ID) && r.Reason != "" }
