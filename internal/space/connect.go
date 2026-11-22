package space

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"unicode/utf8"
)

// Connect frames are [version][kind] and then fixed, ordered fields, each
// a big-endian uint16 length and its bytes; a frame is at most MaxFrame.
const (
	connectVersion  = 1
	MaxFrame        = 4096
	maxReason       = MaxFrame - 4 // version, kind and the reason's length
	connectIDLength = 16
)

// frameKind is a connect frame's second byte.
type frameKind byte

const (
	kindRequest  frameKind = 1
	kindAccepted frameKind = 2
	kindRefused  frameKind = 3
	kindEnded    frameKind = 4
)

// ConnectDomain opens every pairing ID preimage of a space connect.
const ConnectDomain = "pairtask/space-connect/1"

// ErrFrame is a connect frame that isn't exactly one well-formed frame.
var ErrFrame = errors.New("space: malformed connect frame")

// ConnectRequest is a dialer's connect frame: the space both are members
// of, and the attempt's ID, kept for retries.
type ConnectRequest struct {
	Space     string
	ConnectID [16]byte
}

// Encode is the request's frame, or nil for a malformed space ID.
func (r ConnectRequest) Encode() []byte {
	if !validSpaceID(r.Space) {
		return nil
	}
	return frame(kindRequest, []byte(r.Space), r.ConnectID[:])
}

// DecodeConnectRequest reads a connect frame strictly: version 1, kind
// request, a canonical space ID, a 16-byte ID and nothing after them.
func DecodeConnectRequest(b []byte) (ConnectRequest, error) {
	fields, err := wire(b).fields(kindRequest, 2)
	if err != nil || !requestFields(fields) {
		return ConnectRequest{}, ErrFrame
	}
	r := ConnectRequest{Space: string(fields[0])}
	copy(r.ConnectID[:], fields[1])
	return r, nil
}

// requestFields: a canonical space ID and a connect ID of its size.
func requestFields(fields [][]byte) bool {
	return validSpaceID(string(fields[0])) && len(fields[1]) == connectIDLength
}

// ConnectAnswer is the responder's frame: accepted, or refused and why.
//
// Ended answers a retry of an attempt whose pairing the responder
// installed and has since ended: the dialer may forget that attempt and
// begin a new one, which no other refusal allows.
type ConnectAnswer struct {
	Accepted bool
	Reason   string
	Ended    bool
}

var (
	accepted = ConnectAnswer{Accepted: true}
	ended    = ConnectAnswer{Ended: true}
)

// Encode is the answer's frame, or nil for an answer that is none of a
// plain acceptance, a plain ending or a refusal with a valid reason.
func (a ConnectAnswer) Encode() []byte {
	switch {
	case a == accepted:
		return frame(kindAccepted)
	case a == ended:
		return frame(kindEnded)
	case a.refusal():
		return frame(kindRefused, []byte(a.Reason))
	}
	return nil
}

// refusal: only a reason, and a valid one.
func (a ConnectAnswer) refusal() bool { return !a.Accepted && !a.Ended && reason(a.Reason).valid() }

// DecodeConnectAnswer reads an answer frame strictly.
func DecodeConnectAnswer(b []byte) (ConnectAnswer, error) {
	for _, plain := range []struct {
		kind   frameKind
		answer ConnectAnswer
	}{{kindAccepted, accepted}, {kindEnded, ended}} {
		if _, err := wire(b).fields(plain.kind, 0); err == nil {
			return plain.answer, nil
		}
	}
	fields, err := wire(b).fields(kindRefused, 1)
	if err != nil || !reason(fields[0]).valid() {
		return ConnectAnswer{}, ErrFrame
	}
	return ConnectAnswer{Reason: string(fields[0])}, nil
}

// reason is a refusal's text: 1 to maxReason bytes of UTF-8.
type reason string

func (r reason) valid() bool { return r != "" && len(r) <= maxReason && utf8.ValidString(string(r)) }

// frame encodes kind and fields.
func frame(kind frameKind, fields ...[]byte) []byte {
	out := []byte{connectVersion, byte(kind)}
	for _, f := range fields {
		out = binary.BigEndian.AppendUint16(out, uint16(len(f)))
		out = append(out, f...)
	}
	return out
}

// wire is a received frame.
type wire []byte

// fields reads exactly n fields of a kind frame, copied out of w.
func (w wire) fields(kind frameKind, n int) ([][]byte, error) {
	if !w.opens(kind) {
		return nil, ErrFrame
	}
	rest := w[2:]
	fields := make([][]byte, 0, n)
	for range n {
		f, tail, ok := rest.field()
		if !ok {
			return nil, ErrFrame
		}
		fields, rest = append(fields, bytes.Clone(f)), tail
	}
	if len(rest) != 0 {
		return nil, ErrFrame
	}
	return fields, nil
}

// opens: w is at most a frame long and opens with version 1 and kind.
func (w wire) opens(kind frameKind) bool {
	return len(w) <= MaxFrame && bytes.HasPrefix(w, []byte{connectVersion, byte(kind)})
}

// field splits one length-prefixed field off w.
func (w wire) field() ([]byte, wire, bool) {
	if len(w) < 2 {
		return nil, nil, false
	}
	n := int(binary.BigEndian.Uint16(w))
	if len(w)-2 < n {
		return nil, nil, false
	}
	return w[2 : 2+n], w[2+n:], true
}

// PairingID is the pairing both sides of a connect install:
//
//	SHA-256(ConnectDomain ‖ space ‖ low ‖ high ‖ connectID)
//
// space is 32 lowercase hex characters; low and high are the two key IDs
// ("sha256:" and 64 lowercase hex, 71 bytes each) in byte order; every
// field is fixed width, so the preimage is unambiguous. Malformed input is
// an error.
func PairingID(space, a, b string, connectID [16]byte) ([32]byte, error) {
	if !(pairingTuple{space, a, b}).valid() {
		return [32]byte{}, errors.New("space: malformed pairing tuple")
	}
	low, high := min(a, b), max(a, b)
	h := sha256.New()
	for _, part := range [][]byte{[]byte(ConnectDomain), []byte(space), []byte(low), []byte(high), connectID[:]} {
		h.Write(part)
	}
	return [32]byte(h.Sum(nil)), nil
}

// pairingTuple is what a pairing ID binds: the space and both key IDs.
type pairingTuple struct{ space, a, b string }

// valid: a canonical space ID and two canonical key IDs.
func (t pairingTuple) valid() bool {
	return validSpaceID(t.space) && validKeyID(t.a) && validKeyID(t.b)
}
