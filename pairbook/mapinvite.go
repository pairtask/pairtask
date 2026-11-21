package pairbook

import (
	"crypto/ed25519"
	"time"
)

const mapInviteDomain = "pairbook-map-invite-v2"

// MapInvite lets someone ask to join space: the owner signs the hash of a
// one-time token and the map the join is to be made from (Base). It names
// no member and no address: a joiner learns nothing about the space
// before the owner admits them.
type MapInvite struct {
	Space     string
	Base      Mark
	TokenHash [32]byte
	IssuedAt  int64
	ExpiresAt int64
}

// SignedMapInvite is an invite and the owner's signature.
type SignedMapInvite struct {
	MapInvite
	Owner ed25519.PublicKey
	Sig   []byte
}

// SigningBytes are the bytes an invite's signature covers.
func (i MapInvite) SigningBytes() []byte {
	return newEncoder(mapInviteDomain).str(i.Space).count(i.Base.Version).bytes(i.Base.Digest[:]).bytes(i.TokenHash[:]).time(i.IssuedAt).time(i.ExpiresAt).b
}

// SignMapInvite signs i with the owner's person key.
func SignMapInvite(owner ed25519.PrivateKey, i MapInvite) SignedMapInvite {
	return SignedMapInvite{MapInvite: i, Owner: publicOf(owner), Sig: sign(owner, i.SigningBytes())}
}

// Verify checks the invite is space's, signed by the pinned owner key (ID
// ownerKeyID), and valid at now.
func (s SignedMapInvite) Verify(space, ownerKeyID string, now time.Time) error {
	if s.Space != space || !signedBy(s.Owner, s.SigningBytes(), s.Sig) {
		return ErrInvalid
	}
	if !ownerID(ownerKeyID).is(s.Owner) {
		return ErrNotOwner
	}
	return period{s.IssuedAt, s.ExpiresAt}.check(InviteValidity, now)
}
