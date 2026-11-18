package pairbook

import (
	"bytes"
	"crypto/ed25519"
	"net"
	"slices"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"
)

// signedBy: sig is key's valid signature over msg; a key or signature of
// the wrong size is no signature.
func signedBy(key ed25519.PublicKey, msg, sig []byte) bool {
	return len(key) == ed25519.PublicKeySize && len(sig) == ed25519.SignatureSize && ed25519.Verify(key, msg, sig)
}

// sign signs msg with key, or returns nil for a key of the wrong size.
func sign(key ed25519.PrivateKey, msg []byte) []byte {
	if len(key) != ed25519.PrivateKeySize {
		return nil
	}
	return ed25519.Sign(key, msg)
}

// publicOf is key's public half, or nil for a key of the wrong size.
func publicOf(key ed25519.PrivateKey) ed25519.PublicKey {
	if len(key) != ed25519.PrivateKeySize {
		return nil
	}
	return key.Public().(ed25519.PublicKey)
}

func sameKey(a, b ed25519.PublicKey) bool {
	return len(a) == ed25519.PublicKeySize && bytes.Equal(a, b)
}

// ownerID is the key ID a node pinned for its space's owner.
type ownerID string

// is: key is the owner's key.
func (o ownerID) is(key ed25519.PublicKey) bool {
	id := KeyID(key)
	return id != "" && id == string(o)
}

// period is a record's validity in Unix milliseconds: [issued, expires).
type period struct{ issued, expires int64 }

// check: the period is well formed, no longer than limit, and holds now.
func (p period) check(limit time.Duration, now time.Time) error {
	if !p.valid(limit) {
		return ErrInvalid
	}
	if !p.holds(Millis(now)) {
		return ErrExpired
	}
	return nil
}

func (p period) valid(limit time.Duration) bool {
	return p.issued >= 0 && p.expires > p.issued && p.expires-p.issued <= limit.Milliseconds()
}

func (p period) holds(at int64) bool { return at >= p.issued && at < p.expires }

// stamp is a record's moment in Unix milliseconds.
type stamp int64

// near checks the stamp is within limit of now, either way. Both are
// non-negative before any subtraction, so none can overflow.
func (s stamp) near(limit time.Duration, now time.Time) error {
	if s < 0 {
		return ErrInvalid
	}
	current := stamp(Millis(now))
	if current < 0 || s.from(current) > limit.Milliseconds() {
		return ErrExpired
	}
	return nil
}

// from is |s-other| for non-negative stamps.
func (s stamp) from(other stamp) int64 {
	if s > other {
		return int64(s - other)
	}
	return int64(other - s)
}

// validName: 1 to MaxName bytes of UTF-8 without control characters.
func validName(name string) bool {
	return name != "" && len(name) <= MaxName && utf8.ValidString(name) && !slices.ContainsFunc([]rune(name), unicode.IsControl)
}

// addrList is an address record's HOST:PORT entries.
type addrList []string

// valid: 1 to MaxAddresses entries, sorted, unique and each a HOST:PORT.
func (l addrList) valid() bool {
	return l.bounded() && l.unique() && !slices.ContainsFunc(l, invalidHostPort)
}

func (l addrList) bounded() bool {
	return len(l) > 0 && len(l) <= MaxAddresses && slices.IsSorted(l)
}

// unique: no entry repeats; sorted, so only neighbours can.
func (l addrList) unique() bool { return len(slices.Compact(slices.Clone(l))) == len(l) }

func invalidHostPort(a string) bool {
	host, port, err := net.SplitHostPort(a)
	if err != nil || host == "" {
		return true
	}
	n, err := strconv.Atoi(port)
	return err != nil || n < 1 || n > 65535
}
