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

// fieldCodec is a version 8 frame of string fields: its shape, what an
// invalid one is called, a value's fields in order, the value they make,
// and what makes one valid. Encoding and decoding both check it.
type fieldCodec[T any] struct {
	shape  shape
	what   string
	fields func(T) []string
	build  func([]string) T
	valid  func(T) bool
}

func (c fieldCodec[T]) encode(v T) ([]byte, error) {
	if !c.valid(v) {
		return nil, fmt.Errorf("%w: %s", ErrProtocol, c.what)
	}
	var fields [][]byte
	for _, f := range c.fields(v) {
		fields = append(fields, []byte(f))
	}
	return encode(c.shape.kind, fields...)
}

func (c fieldCodec[T]) decode(frame []byte) (T, error) {
	var zero T
	f, err := decode(frame, c.shape)
	if err != nil {
		return zero, err
	}
	fields := make([]string, len(f))
	for i, field := range f {
		fields[i] = string(field)
	}
	v := c.build(fields)
	if !c.valid(v) {
		return zero, fmt.Errorf("%w: %s", ErrProtocol, c.what)
	}
	return v, nil
}

var (
	heldCodec = fieldCodec[Held]{heldShape, "held needs both IDs",
		func(h Held) []string { return []string{h.ID, h.HeldID} },
		func(f []string) Held { return Held{ID: f[0], HeldID: f[1]} },
		func(h Held) bool { return ValidID(h.ID) && ValidID(h.HeldID) }}
	consentOutcomeCodec = fieldCodec[ConsentOutcome]{consentOutcomeShape, "consent outcome",
		func(o ConsentOutcome) []string { return []string{o.ID, o.State, o.Reason} },
		func(f []string) ConsentOutcome { return ConsentOutcome{ID: f[0], State: f[1], Reason: f[2]} },
		validOutcome}
	consentStoredCodec = fieldCodec[ConsentOutcomeStored]{consentOutcomeStoredShape, "consent outcome answer needs its ID",
		func(s ConsentOutcomeStored) []string { return []string{s.ID} },
		func(f []string) ConsentOutcomeStored { return ConsentOutcomeStored{ID: f[0]} },
		func(s ConsentOutcomeStored) bool { return ValidID(s.ID) }}
	consentRefusedCodec = fieldCodec[ConsentOutcomeRefused]{consentRefusedShape, "consent outcome refusal",
		func(r ConsentOutcomeRefused) []string { return []string{r.ID, r.Reason} },
		func(f []string) ConsentOutcomeRefused { return ConsentOutcomeRefused{ID: f[0], Reason: f[1]} },
		validConsentRefusal}
)

func EncodeHeld(h Held) ([]byte, error) { return heldCodec.encode(h) }

func DecodeHeld(frame []byte) (Held, error) { return heldCodec.decode(frame) }

func EncodeConsentOutcome(o ConsentOutcome) ([]byte, error) { return consentOutcomeCodec.encode(o) }

func DecodeConsentOutcome(frame []byte) (ConsentOutcome, error) {
	return consentOutcomeCodec.decode(frame)
}

// validOutcome: a request ID and one of the outcomes a hold can end in.
func validOutcome(o ConsentOutcome) bool {
	return ValidID(o.ID) && consentStates[o.State]
}

func EncodeConsentOutcomeStored(s ConsentOutcomeStored) ([]byte, error) {
	return consentStoredCodec.encode(s)
}

func DecodeConsentOutcomeStored(frame []byte) (ConsentOutcomeStored, error) {
	return consentStoredCodec.decode(frame)
}

func EncodeConsentOutcomeRefused(r ConsentOutcomeRefused) ([]byte, error) {
	return consentRefusedCodec.encode(r)
}

func DecodeConsentOutcomeRefused(frame []byte) (ConsentOutcomeRefused, error) {
	return consentRefusedCodec.decode(frame)
}

// validConsentRefusal: a request ID and a reason.
func validConsentRefusal(r ConsentOutcomeRefused) bool { return ValidID(r.ID) && r.Reason != "" }
