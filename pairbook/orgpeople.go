package pairbook

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"slices"
)

// Organization people (designs/pairbook/org-persons.md, public wire v1).
// In a company space a person is one verified account of the organization:
// the Pairbook keeps which account, and the map carries only an opaque,
// random, per-space PersonID. The organization vouches for the person with
// the map's own signature, by the space key; a machine joins with its
// workspace key's OrgProof alone, so no person key exists on any machine.
//
// A map holding org people is format 3 and signs under its own domain, so
// a client that predates it fails to verify it and grants nothing. Format 2
// maps keep their bytes and JSON exactly.

// Map formats.
const (
	MapFormatKey uint8 = 2 // key people only; also a map with no Format
	MapFormatOrg uint8 = 3 // may hold org people
)

// KindOrg marks an org person, and a binding to one.
const KindOrg = "org"

// SelectOrg is the grant selector kind naming one org person by PersonID
// (hex). Only format-3 maps use it.
const SelectOrg = "org"

const (
	mapDomainV3    = "pairbook-map-v3"
	orgProofDomain = "pairbook-org-proof-v1"
	orgKeyDomain   = "pairbook-org-person-v1"
)

// PersonID names an org person within one space: 16 random bytes, never
// derived from the account.
type PersonID [16]byte

// String is id in hex, as a selector names it.
func (id PersonID) String() string { return hex.EncodeToString(id[:]) }

// Selector is how status and policy name the person: "org:" and the hex.
func (id PersonID) Selector() string { return SelectOrg + ":" + id.String() }

// Selector names p for status and policy: "org:<hex>" for an org person,
// the lineage hex for a key person. Never use an org person's Lineage,
// which is zero on the wire.
func (p MapPerson) Selector() string {
	if p.Kind == KindOrg {
		return p.PersonID.Selector()
	}
	return LineageHex(p.Lineage)
}

// orgKey is the key a verified map indexes an org person by, in place of a
// lineage: a hash under its own domain, so it is never zero and never
// equal to a real lineage or to another space's person.
func orgKey(space string, id PersonID) Lineage {
	return sha256.Sum256(newEncoder(orgKeyDomain).str(space).bytes(id[:]).b)
}

func personIDOf(s string) (PersonID, bool) {
	var id PersonID
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(id) {
		return id, false
	}
	copy(id[:], b)
	return id, id != PersonID{}
}

// OrgProof is a workspace's own statement that it joins space: signed by
// the workspace key before any account is known, over the Pairbook's
// one-time nonce (the enrollment challenge, or a carryover challenge).
type OrgProof struct {
	Space     string
	Workspace string // the workspace key's ID
	Nonce     [16]byte
	At        int64
}

// SignedOrgProof is an OrgProof and the workspace key's signature.
type SignedOrgProof struct {
	OrgProof
	Sig []byte
}

// SigningBytes are the bytes an OrgProof's signature covers.
func (p OrgProof) SigningBytes() []byte {
	return newEncoder(orgProofDomain).str(p.Space).str(p.Workspace).bytes(p.Nonce[:]).time(p.At).b
}

// SignOrgProof signs p with the workspace key.
func SignOrgProof(w ed25519.PrivateKey, p OrgProof) SignedOrgProof {
	return SignedOrgProof{OrgProof: p, Sig: sign(w, p.SigningBytes())}
}

// Marshal is the proof as a binding carries it: its signing bytes, then
// the 64-byte signature.
func (s SignedOrgProof) Marshal() []byte {
	return append(s.SigningBytes(), s.Sig...)
}

// ParseOrgProof reads a marshalled proof. It checks the form only; Verify
// checks the signature.
func ParseOrgProof(b []byte) (SignedOrgProof, error) {
	if len(b) < ed25519.SignatureSize {
		return SignedOrgProof{}, ErrInvalid
	}
	body, sig := b[:len(b)-ed25519.SignatureSize], b[len(b)-ed25519.SignatureSize:]
	p, ok := decodeOrgProof(body)
	if !ok || !slices.Equal(p.SigningBytes(), body) {
		return SignedOrgProof{}, ErrInvalid
	}
	return SignedOrgProof{OrgProof: p, Sig: slices.Clone(sig)}, nil
}

// Verify checks the proof is space's, names w and is signed by it.
func (s SignedOrgProof) Verify(space string, w ed25519.PublicKey) error {
	if s.Space != space || s.Workspace != KeyID(w) {
		return ErrInvalid
	}
	if !signedBy(w, s.SigningBytes(), s.Sig) {
		return ErrInvalid
	}
	return nil
}

func decodeOrgProof(b []byte) (OrgProof, bool) {
	d := decoder{b: b, ok: true}
	var p OrgProof
	domain := d.str()
	p.Space, p.Workspace = d.str(), d.str()
	nonce := d.field()
	p.At = int64(d.u64())
	if !d.ok || len(d.b) != 0 {
		return OrgProof{}, false
	}
	if domain != orgProofDomain || len(nonce) != len(p.Nonce) {
		return OrgProof{}, false
	}
	copy(p.Nonce[:], nonce)
	return p, true
}

// decoder reads what encoder writes; ok turns false at the first short read.
type decoder struct {
	b  []byte
	ok bool
}

func (d *decoder) field() []byte {
	if !d.ok || len(d.b) < 4 {
		return d.fail()
	}
	n := binary.BigEndian.Uint32(d.b)
	if uint64(len(d.b)-4) < uint64(n) {
		return d.fail()
	}
	f := d.b[4 : 4+n]
	d.b = d.b[4+n:]
	return f
}

func (d *decoder) str() string { return string(d.field()) }

func (d *decoder) u64() uint64 {
	if !d.ok || len(d.b) < 8 {
		d.fail()
		return 0
	}
	v := binary.BigEndian.Uint64(d.b)
	d.b = d.b[8:]
	return v
}

func (d *decoder) fail() []byte {
	d.ok = false
	d.b = nil
	return nil
}

func (e *encoder) personIDs(ids []PersonID) *encoder {
	e.count(uint64(len(ids)))
	for _, id := range ids {
		e.bytes(id[:])
	}
	return e
}

// empty: no evidence at all, as an org person carries.
func (ev PersonEvidence) empty() bool {
	return len(ev.Anchor) == 0 && len(ev.Statements) == 0 && len(ev.Workspaces) == 0
}
