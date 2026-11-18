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
	kindRequest     = 1
	kindAccepted    = 2
	kindRefused     = 3
	MaxFrame        = 4096
	maxReason       = MaxFrame - 4 // version, kind and the reason's length
	connectIDLength = 16
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
	fields, err := unframe(b, kindRequest, 2)
	if err != nil || !validSpaceID(string(fields[0])) || len(fields[1]) != connectIDLength {
		return ConnectRequest{}, ErrFrame
	}
	r := ConnectRequest{Space: string(fields[0])}
	copy(r.ConnectID[:], fields[1])
	return r, nil
}

// ConnectAnswer is the responder's frame: accepted, or refused and why.
type ConnectAnswer struct {
	Accepted bool
	Reason   string
}

// Encode is the answer's frame, or nil for an answer that is neither a
// plain acceptance nor a refusal with a valid reason.
func (a ConnectAnswer) Encode() []byte {
	switch {
	case a.Accepted && a.Reason == "":
		return frame(kindAccepted)
	case !a.Accepted && validReason(a.Reason):
		return frame(kindRefused, []byte(a.Reason))
	}
	return nil
}

// DecodeConnectAnswer reads an answer frame strictly.
func DecodeConnectAnswer(b []byte) (ConnectAnswer, error) {
	if _, err := unframe(b, kindAccepted, 0); err == nil {
		return ConnectAnswer{Accepted: true}, nil
	}
	fields, err := unframe(b, kindRefused, 1)
	if err != nil || !validReason(string(fields[0])) {
		return ConnectAnswer{}, ErrFrame
	}
	return ConnectAnswer{Reason: string(fields[0])}, nil
}

func validReason(r string) bool { return r != "" && len(r) <= maxReason && utf8.ValidString(r) }

// frame encodes kind and fields.
func frame(kind byte, fields ...[]byte) []byte {
	out := []byte{connectVersion, kind}
	for _, f := range fields {
		out = binary.BigEndian.AppendUint16(out, uint16(len(f)))
		out = append(out, f...)
	}
	return out
}

// unframe reads exactly n fields of a kind frame, copied out of b.
func unframe(b []byte, kind byte, n int) ([][]byte, error) {
	if len(b) > MaxFrame || len(b) < 2 || b[0] != connectVersion || b[1] != kind {
		return nil, ErrFrame
	}
	rest := b[2:]
	fields := make([][]byte, 0, n)
	for range n {
		f, tail, ok := field(rest)
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

// field splits one length-prefixed field off b.
func field(b []byte) ([]byte, []byte, bool) {
	if len(b) < 2 {
		return nil, nil, false
	}
	n := int(binary.BigEndian.Uint16(b))
	if len(b)-2 < n {
		return nil, nil, false
	}
	return b[2 : 2+n], b[2+n:], true
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
	if !validSpaceID(space) || !validKeyID(a) || !validKeyID(b) {
		return [32]byte{}, errors.New("space: malformed pairing tuple")
	}
	low, high := min(a, b), max(a, b)
	h := sha256.New()
	for _, part := range [][]byte{[]byte(ConnectDomain), []byte(space), []byte(low), []byte(high), connectID[:]} {
		h.Write(part)
	}
	return [32]byte(h.Sum(nil)), nil
}
