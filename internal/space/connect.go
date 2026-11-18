package space

import "errors"

var errConnectNotImplemented = errors.New("space: connect not implemented")

// ConnectDomain opens every pairing ID preimage of a space connect.
const ConnectDomain = "pairtask/space-connect/1"

// ConnectRequest is a dialer's connect frame: the space both are members
// of, and the attempt's ID, kept for retries.
type ConnectRequest struct {
	Space     string
	ConnectID [16]byte
}

// Encode is the request's frame.
func (r ConnectRequest) Encode() []byte { return nil }

// DecodeConnectRequest reads a connect frame strictly.
func DecodeConnectRequest(b []byte) (ConnectRequest, error) {
	return ConnectRequest{}, errConnectNotImplemented
}

// ConnectAnswer is the responder's frame: accepted, or refused and why.
type ConnectAnswer struct {
	Accepted bool
	Reason   string
}

// Encode is the answer's frame.
func (a ConnectAnswer) Encode() []byte { return nil }

// DecodeConnectAnswer reads an answer frame strictly.
func DecodeConnectAnswer(b []byte) (ConnectAnswer, error) {
	return ConnectAnswer{}, errConnectNotImplemented
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
	return [32]byte{}, errConnectNotImplemented
}
