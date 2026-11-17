package pairbook

import (
	"crypto/ed25519"
	"encoding/binary"
	"time"

	"github.com/pairtask/pairtask/internal/signing"
)

// Domains separate the bytes each kind of record signs, so a signature
// over one kind never verifies as another.
const (
	inviteDomain     = "pairbook-invite-v1"
	joinDomain       = "pairbook-join-v1"
	memberDomain     = "pairbook-member-v1"
	addressDomain    = "pairbook-address-v1"
	revocationDomain = "pairbook-revocation-v1"
	httpDomain       = "pairbook-http-v1"
)

// KeyID is pub's ID: "sha256:" + hex(SHA-256(SPKI DER)), as pairing pins it.
func KeyID(pub ed25519.PublicKey) string {
	if len(pub) != ed25519.PublicKeySize {
		return ""
	}
	return signing.KeyID(pub)
}

// Millis is t as the Unix milliseconds records carry.
func Millis(t time.Time) int64 { return t.UnixMilli() }

// encoder appends a record's fields: each bytes-like field as a u32
// big-endian length and the bytes, each time as a u64 big-endian.
type encoder struct{ b []byte }

func newEncoder(domain string) *encoder {
	e := &encoder{}
	e.str(domain)
	return e
}

func (e *encoder) bytes(p []byte) *encoder {
	e.b = binary.BigEndian.AppendUint32(e.b, uint32(len(p)))
	e.b = append(e.b, p...)
	return e
}

func (e *encoder) str(s string) *encoder { return e.bytes([]byte(s)) }

func (e *encoder) time(ms int64) *encoder {
	e.b = binary.BigEndian.AppendUint64(e.b, uint64(ms))
	return e
}

func (e *encoder) strs(list []string) *encoder {
	e.b = binary.BigEndian.AppendUint32(e.b, uint32(len(list)))
	for _, s := range list {
		e.str(s)
	}
	return e
}
