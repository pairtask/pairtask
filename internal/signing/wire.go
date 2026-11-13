// Package signing signs messages end to end (#115): who sent a message is
// checked against keys the recipient pinned or approved, never taken from a
// broker. It is pure: keys, certificates, envelopes and verification, with
// no storage or transport.
//
// Signed bytes are a fixed layout of length-prefixed fields after a domain
// string, never re-serialised JSON, so two implementations agree on them
// byte for byte and a signature for one purpose never verifies for another.
package signing

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"errors"
)

// KeyID is a public key's pairtask fingerprint: "sha256:" and the hex
// SHA-256 of its SPKI DER, the same form as a workspace's identity key.
func KeyID(pub ed25519.PublicKey) string {
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(spki)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// maxField bounds one field when decoding.
const maxField = 1 << 20

var errMalformed = errors.New("signing: malformed encoding")

// writer appends length-prefixed fields.
type writer struct{ b []byte }

func (w *writer) bytes(p []byte) {
	w.b = binary.BigEndian.AppendUint32(w.b, uint32(len(p)))
	w.b = append(w.b, p...)
}

func (w *writer) str(s string) { w.bytes([]byte(s)) }

func (w *writer) u64(v uint64) { w.b = binary.BigEndian.AppendUint64(w.b, v) }

// reader takes the fields back in order; the first problem sticks.
type reader struct {
	b   []byte
	err error
}

func (r *reader) bytes() []byte {
	if r.err != nil || len(r.b) < 4 {
		r.fail()
		return nil
	}
	n := binary.BigEndian.Uint32(r.b)
	if n > maxField || uint64(len(r.b)-4) < uint64(n) {
		r.fail()
		return nil
	}
	p := r.b[4 : 4+n]
	r.b = r.b[4+n:]
	return p
}

func (r *reader) str() string { return string(r.bytes()) }

func (r *reader) u64() uint64 {
	if r.err != nil || len(r.b) < 8 {
		r.fail()
		return 0
	}
	v := binary.BigEndian.Uint64(r.b)
	r.b = r.b[8:]
	return v
}

func (r *reader) fail() {
	if r.err == nil {
		r.err = errMalformed
	}
	r.b = nil
}

// done is the reader's error, or errMalformed for trailing bytes.
func (r *reader) done() error {
	if r.err == nil && len(r.b) != 0 {
		r.err = errMalformed
	}
	return r.err
}
