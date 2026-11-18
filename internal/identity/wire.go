// Package identity is one person across machines (#392): the signed
// statements a person key makes about its devices, workspaces, recovery
// and successors, the state a peer holds about a person, and how a received
// seal is admitted against it. The contract is designs/identity/wire-v0.md.
//
// The package is pure, like signing: no storage, no transport. Signed bytes
// are a domain string followed by length-prefixed fields and big-endian
// integers, as signing has them, so a signature for one statement never
// verifies for another.
package identity

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"time"
)

// Lineage identifies a person across successions: the digest of the anchor.
type Lineage [32]byte

func (l Lineage) String() string { return "lineage:" + hex.EncodeToString(l[:]) }

// Digest is the SHA-256 of a statement's signed bytes.
type Digest [32]byte

func (d Digest) String() string { return hex.EncodeToString(d[:]) }

// DigestOf is the digest of signed bytes.
func DigestOf(tbs []byte) Digest { return sha256.Sum256(tbs) }

var (
	// ErrMalformed: bytes that aren't a statement of the kind asked for.
	ErrMalformed = errors.New("identity: malformed statement")
	// ErrBadSignature: a signature that doesn't verify under the key given.
	ErrBadSignature = errors.New("identity: bad signature")
	// ErrLimit: a count or size past the contract's limits.
	ErrLimit = errors.New("identity: over a limit")
	// ErrFreshKey: a successor key the lineage has used before.
	ErrFreshKey = errors.New("identity: successor key is not fresh")
	// ErrNotImplemented marks behaviour the skeleton declares but doesn't do yet.
	ErrNotImplemented = errors.New("identity: not implemented")
)

// Limits of wire-v0 §2.
const (
	MaxDevices     = 64
	MaxBindings    = 64
	MaxRevocations = 4096
	MaxLabel       = 120
	MaxCounter     = 1 << 62 // epochs, counters, sequence and version numbers
	maxField       = 1 << 20
)

// writer appends length-prefixed fields.
type writer struct{ b []byte }

func (w *writer) bytes(p []byte) {
	w.b = binary.BigEndian.AppendUint32(w.b, uint32(len(p)))
	w.b = append(w.b, p...)
}

func (w *writer) str(s string)            { w.bytes([]byte(s)) }
func (w *writer) u64(v uint64)            { w.b = binary.BigEndian.AppendUint64(w.b, v) }
func (w *writer) key(k ed25519.PublicKey) { w.bytes(k) }
func (w *writer) lineage(l Lineage)       { w.bytes(l[:]) }
func (w *writer) digest(d Digest)         { w.bytes(d[:]) }
func (w *writer) at(t time.Time)          { w.u64(uint64(t.UnixMilli())) }
func (w *writer) count(n int)             { w.u64(uint64(n)) }
func (w *writer) ids(ids []string) {
	w.count(len(ids))
	for _, id := range ids {
		w.str(id)
	}
}
func (w *writer) digests(ds []Digest) {
	w.count(len(ds))
	for _, d := range ds {
		w.digest(d)
	}
}
func (w *writer) domain(d string) *writer { w.str(d); return w }

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

// counter is a u64 within MaxCounter.
func (r *reader) counter() uint64 {
	v := r.u64()
	if v > MaxCounter {
		r.fail()
	}
	return v
}

func (r *reader) key() ed25519.PublicKey {
	k := r.bytes()
	if r.err == nil && len(k) != ed25519.PublicKeySize {
		r.fail()
	}
	return ed25519.PublicKey(k)
}

func (r *reader) fixed32() [32]byte {
	var out [32]byte
	p := r.bytes()
	if r.err == nil && len(p) != 32 {
		r.fail()
	}
	copy(out[:], p)
	return out
}

func (r *reader) lineage() Lineage { return Lineage(r.fixed32()) }
func (r *reader) digest() Digest   { return Digest(r.fixed32()) }
func (r *reader) at() time.Time    { return time.UnixMilli(int64(r.u64())).UTC() }

// count is an element count within limit.
func (r *reader) count(limit int) int {
	n := r.u64()
	if n > uint64(limit) {
		r.fail()
		return 0
	}
	return int(n)
}

// ids reads a sorted, duplicate-free list of key IDs.
func (r *reader) ids(limit int) []string {
	n := r.count(limit)
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, r.str())
	}
	if !sortedUnique(out) {
		r.fail()
	}
	return out
}

func (r *reader) digests(limit int) []Digest {
	n := r.count(limit)
	out := make([]Digest, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, r.digest())
	}
	return out
}

// domain reads the first field and requires it to be d.
func (r *reader) domain(d string) {
	if r.str() != d {
		r.fail()
	}
}

func (r *reader) fail() {
	if r.err == nil {
		r.err = ErrMalformed
	}
	r.b = nil
}

// done is the reader's error, or ErrMalformed for trailing bytes.
func (r *reader) done() error {
	if r.err == nil && len(r.b) != 0 {
		r.err = ErrMalformed
	}
	return r.err
}

// sortedUnique: strictly increasing, so canonical.
func sortedUnique(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i] <= s[i-1] {
			return false
		}
	}
	return true
}

// envelope splits Marshal'd bytes into signed bytes and signature.
func envelope(b []byte) (tbs, sig []byte, err error) {
	outer := reader{b: b}
	tbs, sig = outer.bytes(), outer.bytes()
	if err := outer.done(); err != nil {
		return nil, nil, err
	}
	if len(sig) != ed25519.SignatureSize {
		return nil, nil, ErrMalformed
	}
	return tbs, sig, nil
}

func sealed(tbs, sig []byte) []byte {
	w := writer{}
	w.bytes(tbs)
	w.bytes(sig)
	return w.b
}
