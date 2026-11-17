// Package pairbook is the wire contract of Pairbook, the directory a
// pairtask space's members use to find each other's current address.
//
// Every record is signed over domain-separated, length-prefixed bytes,
// never over JSON. Every record names its space. A member record counts only
// when the space owner signed it, so the directory can withhold records but
// can't add or fake a member. Keys are Ed25519; a key's ID is pairtask's
// usual "sha256:" + hex(SHA-256(SPKI DER)). Times are Unix milliseconds.
//
// This package checks records. It keeps no state: replay protection for
// signed HTTP requests is the server's, which stores ReplayKey until
// RetainUntil.
package pairbook

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Validity bounds and limits.
const (
	InviteValidity  = 7 * 24 * time.Hour  // an invite's ExpiresAt - IssuedAt, at most
	MemberValidity  = 90 * 24 * time.Hour // a member record's ExpiresAt - IssuedAt, at most
	AddressValidity = 24 * time.Hour      // an address record's ExpiresAt - IssuedAt, at most
	JoinFreshness   = 10 * time.Minute    // how far a join request's At may be from now
	RequestSkew     = 5 * time.Minute     // how far a signed HTTP request's At may be from now
	MaxAddresses    = 8                   // addresses in one record
	MaxName         = 64                  // bytes in a display name
)

// Roles a member record grants. A space has one owner: its record's Node
// is the owner key itself.
const (
	RoleOwner  = "owner"
	RoleMember = "member"
)

// Errors.
var (
	ErrNotImplemented = errors.New("pairbook: not implemented")
	ErrInvalid        = errors.New("pairbook: invalid record") // malformed, wrong space, bad key or signature
	ErrExpired        = errors.New("pairbook: expired or not yet valid")
	ErrNotOwner       = errors.New("pairbook: not signed by the space owner")
)

// Invite is what the owner's node issues: the hash of a one-time join token.
type Invite struct {
	Space     string
	TokenHash [32]byte
	IssuedAt  int64
	ExpiresAt int64
}

// SignedInvite is an invite and the owner's signature.
type SignedInvite struct {
	Invite
	Owner ed25519.PublicKey
	Sig   []byte
}

// SigningBytes are the bytes an invite's signature covers.
func (i Invite) SigningBytes() []byte {
	return newEncoder(inviteDomain).str(i.Space).bytes(i.TokenHash[:]).time(i.IssuedAt).time(i.ExpiresAt).b
}

// SignInvite signs i with the owner key.
func SignInvite(owner ed25519.PrivateKey, i Invite) SignedInvite {
	return SignedInvite{Invite: i, Owner: publicOf(owner), Sig: sign(owner, i.SigningBytes())}
}

// Verify checks the invite belongs to space, is signed by the owner key with
// ID ownerKeyID, and is valid at now.
func (s SignedInvite) Verify(space, ownerKeyID string, now time.Time) error {
	if s.Space != space || !s.wellFormed() {
		return ErrInvalid
	}
	if !ownerID(ownerKeyID).is(s.Owner) {
		return ErrNotOwner
	}
	return period{s.IssuedAt, s.ExpiresAt}.check(InviteValidity, now)
}

func (s SignedInvite) wellFormed() bool {
	return signedBy(s.Owner, s.SigningBytes(), s.Sig)
}

// JoinRequest is what a joining node sends: its key and name, the token's
// hash, and when it asked.
type JoinRequest struct {
	Space     string
	Node      ed25519.PublicKey
	Name      string
	TokenHash [32]byte
	At        int64
}

// SignedJoinRequest is a join request signed by its own node key.
type SignedJoinRequest struct {
	JoinRequest
	Sig []byte
}

// SigningBytes are the bytes a join request's signature covers.
func (r JoinRequest) SigningBytes() []byte {
	return newEncoder(joinDomain).str(r.Space).bytes(r.Node).str(r.Name).bytes(r.TokenHash[:]).time(r.At).b
}

// SignJoinRequest signs r with the joining node's key.
func SignJoinRequest(node ed25519.PrivateKey, r JoinRequest) SignedJoinRequest {
	return SignedJoinRequest{JoinRequest: r, Sig: sign(node, r.SigningBytes())}
}

// Verify checks the request belongs to space, is signed by its Node, and
// its At is within JoinFreshness of now.
func (s SignedJoinRequest) Verify(space string, now time.Time) error {
	if s.Space != space || !s.wellFormed() {
		return ErrInvalid
	}
	return stamp(s.At).near(JoinFreshness, now)
}

func (s SignedJoinRequest) wellFormed() bool {
	return validName(s.Name) && signedBy(s.Node, s.SigningBytes(), s.Sig)
}

// Member is a node admitted to a space by its owner.
type Member struct {
	Space     string
	Node      ed25519.PublicKey
	Name      string
	Role      string
	IssuedAt  int64
	ExpiresAt int64
}

// SignedMember is a member record and the owner's signature.
type SignedMember struct {
	Member
	Owner ed25519.PublicKey
	Sig   []byte
}

// SigningBytes are the bytes a member record's signature covers.
func (m Member) SigningBytes() []byte {
	return newEncoder(memberDomain).str(m.Space).bytes(m.Node).str(m.Name).str(m.Role).time(m.IssuedAt).time(m.ExpiresAt).b
}

// SignMember signs m with the owner key.
func SignMember(owner ed25519.PrivateKey, m Member) SignedMember {
	return SignedMember{Member: m, Owner: publicOf(owner), Sig: sign(owner, m.SigningBytes())}
}

// Verify checks the record belongs to space, is signed by the owner key with
// ID ownerKeyID, has a valid role (the owner role only for the owner key's
// own record), and is valid at now.
func (s SignedMember) Verify(space, ownerKeyID string, now time.Time) error {
	if s.Space != space || !s.wellFormed() {
		return ErrInvalid
	}
	if !ownerID(ownerKeyID).is(s.Owner) {
		return ErrNotOwner
	}
	return period{s.IssuedAt, s.ExpiresAt}.check(MemberValidity, now)
}

func (s SignedMember) wellFormed() bool {
	return validName(s.Name) && s.validRole() && signedBy(s.Owner, s.SigningBytes(), s.Sig)
}

// validRole: a member's role, or the owner role on the owner key's own
// record.
func (s SignedMember) validRole() bool {
	switch s.Role {
	case RoleMember:
		return len(s.Node) == ed25519.PublicKeySize
	case RoleOwner:
		return sameKey(s.Node, s.Owner)
	}
	return false
}

// Address is where a node can be reached now: HOST:PORT strings, sorted.
type Address struct {
	Space     string
	Node      ed25519.PublicKey
	Addrs     []string
	IssuedAt  int64
	ExpiresAt int64
}

// SignedAddress is an address record signed by its own node key.
type SignedAddress struct {
	Address
	Sig []byte
}

// SigningBytes are the bytes an address record's signature covers.
func (a Address) SigningBytes() []byte {
	return newEncoder(addressDomain).str(a.Space).bytes(a.Node).strs(a.Addrs).time(a.IssuedAt).time(a.ExpiresAt).b
}

// SignAddress signs a with the node's key.
func SignAddress(node ed25519.PrivateKey, a Address) SignedAddress {
	return SignedAddress{Address: a, Sig: sign(node, a.SigningBytes())}
}

// Verify checks the record belongs to space, is signed by its Node, holds
// 1 to MaxAddresses valid HOST:PORT entries, and is valid at now. Whether
// Node is a member is the caller's check, against a verified roster.
func (s SignedAddress) Verify(space string, now time.Time) error {
	if s.Space != space || !s.wellFormed() {
		return ErrInvalid
	}
	return period{s.IssuedAt, s.ExpiresAt}.check(AddressValidity, now)
}

func (s SignedAddress) wellFormed() bool {
	return addrList(s.Addrs).valid() && signedBy(s.Node, s.SigningBytes(), s.Sig)
}

// Revocation removes Node from a space: signed by By, the owner or Node
// itself (leaving).
type Revocation struct {
	Space string
	Node  ed25519.PublicKey
	By    ed25519.PublicKey
	At    int64
}

// SignedRevocation is a revocation signed by its By key.
type SignedRevocation struct {
	Revocation
	Sig []byte
}

// SigningBytes are the bytes a revocation's signature covers.
func (r Revocation) SigningBytes() []byte {
	return newEncoder(revocationDomain).str(r.Space).bytes(r.Node).bytes(r.By).time(r.At).b
}

// SignRevocation signs r with by, which must be r.By's private key.
func SignRevocation(by ed25519.PrivateKey, r Revocation) SignedRevocation {
	return SignedRevocation{Revocation: r, Sig: sign(by, r.SigningBytes())}
}

// Verify checks the revocation belongs to space, is signed by By, and By
// is the owner key with ID ownerKeyID or Node itself.
func (s SignedRevocation) Verify(space, ownerKeyID string) error {
	if s.Space != space || !s.wellFormed() {
		return ErrInvalid
	}
	if !s.authorised(ownerID(ownerKeyID)) {
		return ErrNotOwner
	}
	return nil
}

func (s SignedRevocation) wellFormed() bool {
	return s.At >= 0 && len(s.Node) == ed25519.PublicKeySize && signedBy(s.By, s.SigningBytes(), s.Sig)
}

// authorised: the owner revokes, or a member removes itself.
func (s SignedRevocation) authorised(owner ownerID) bool {
	return owner.is(s.By) || sameKey(s.By, s.Node)
}

// Roster is a space's member records and revocations as the directory
// returns them, not yet trusted.
type Roster struct {
	Members     []SignedMember
	Revocations []SignedRevocation
}

// Verified is the members a node may trust at now: records of space signed
// by the owner key with ID ownerKeyID, valid at now, and not revoked by a
// valid revocation at or after their IssuedAt. Anything else is dropped.
func (r Roster) Verified(space, ownerKeyID string, now time.Time) []Member {
	revoked := r.revokedAt(scope{space, ownerID(ownerKeyID)})
	latest := map[string]Member{}
	for _, m := range r.Members {
		if m.Verify(space, ownerKeyID, now) != nil || revoked.removes(m.Member) {
			continue
		}
		id := KeyID(m.Node)
		if held, ok := latest[id]; !ok || m.IssuedAt > held.IssuedAt {
			latest[id] = m.Member
		}
	}
	return sortedMembers(latest)
}

// revokedAt is each node's latest valid revocation time, by key ID.
func (r Roster) revokedAt(sc scope) revocations {
	at := revocations{}
	for _, v := range r.Revocations {
		id := KeyID(v.Node)
		if v.Verify(sc.space, string(sc.owner)) == nil && v.At >= at[id] {
			at[id] = v.At
		}
	}
	return at
}

// scope is the space a roster is read for and its pinned owner.
type scope struct {
	space string
	owner ownerID
}

// revocations is each node's latest valid revocation time, by key ID.
type revocations map[string]int64

// removes: a revocation at or after the record's IssuedAt removes it.
func (r revocations) removes(m Member) bool {
	at, ok := r[KeyID(m.Node)]
	return ok && at >= m.IssuedAt
}

// sortedMembers is the members by key ID, so a roster reads the same
// whatever order the directory sent.
func sortedMembers(byID map[string]Member) []Member {
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]Member, 0, len(ids))
	for _, id := range ids {
		out = append(out, byID[id])
	}
	return out
}

// HTTPAuth signs one HTTP request: the method, the exact request target
// (escaped path and raw query, as sent), the body's hash, the time and a
// random nonce.
type HTTPAuth struct {
	Key      ed25519.PublicKey
	Method   string
	Target   string
	BodyHash [32]byte
	At       int64
	Nonce    [16]byte
	Sig      []byte
}

// SigningBytes are the bytes an HTTP request's signature covers.
func (a HTTPAuth) SigningBytes() []byte {
	return newEncoder(httpDomain).bytes(a.Key).str(a.Method).str(a.Target).bytes(a.BodyHash[:]).time(a.At).bytes(a.Nonce[:]).b
}

// HTTPRequest is what SignHTTP signs: the method, the exact request target
// as sent, the body and a random nonce.
type HTTPRequest struct {
	Method string
	Target string
	Body   []byte
	Nonce  [16]byte
}

// SignHTTP signs r at now.
func SignHTTP(key ed25519.PrivateKey, r HTTPRequest, now time.Time) HTTPAuth {
	a := HTTPAuth{Key: publicOf(key), Method: r.Method, Target: r.Target, BodyHash: sha256.Sum256(r.Body), At: Millis(now), Nonce: r.Nonce}
	a.Sig = sign(key, a.SigningBytes())
	return a
}

// Verify checks the auth signs exactly this method, target and body, that
// the target is canonical (no "..", empty segment or fragment), and that At
// is within RequestSkew of now. Replays are the server's to refuse.
func (a HTTPAuth) Verify(method, target string, body []byte, now time.Time) error {
	if !a.wellFormed(HTTPRequest{Method: method, Target: target, Body: body, Nonce: a.Nonce}) {
		return ErrInvalid
	}
	return stamp(a.At).near(RequestSkew, now)
}

func (a HTTPAuth) wellFormed(r HTTPRequest) bool {
	return a.covers(r) && requestTarget(r.Target).canonical() && signedBy(a.Key, a.SigningBytes(), a.Sig)
}

// covers: the auth names exactly this method, target and body.
func (a HTTPAuth) covers(r HTTPRequest) bool {
	return a.Method == r.Method && a.Target == r.Target && a.BodyHash == sha256.Sum256(r.Body)
}

// requestTarget is an HTTP request target exactly as sent.
type requestTarget string

// canonical: an absolute path, then an optional query, with no
// fragment, every escape valid, and no path segment that is empty, a dot
// segment or holds a separator once decoded. The signature still covers
// the raw target, exactly as sent.
func (t requestTarget) canonical() bool {
	path, query, _ := strings.Cut(string(t), "?")
	if !strings.HasPrefix(path, "/") || strings.Contains(string(t), "#") {
		return false
	}
	if _, err := url.ParseQuery(query); err != nil {
		return false
	}
	for _, seg := range strings.Split(path[1:], "/") {
		if !pathSegment(seg).plain() {
			return false
		}
	}
	return true
}

// pathSegment is one segment of a request target's path, still escaped.
type pathSegment string

// plain: one path segment that decodes cleanly to a name, never to
// "", ".", ".." or anything holding a separator.
func (seg pathSegment) plain() bool {
	decoded, err := url.PathUnescape(string(seg))
	if err != nil {
		return false
	}
	return decoded != "" && decoded != "." && decoded != ".." && !strings.ContainsAny(decoded, "/\\")
}

// ReplayKey identifies the request for replay refusal: its key ID and nonce.
func (a HTTPAuth) ReplayKey() string { return KeyID(a.Key) + ":" + hex.EncodeToString(a.Nonce[:]) }

// RetainUntil is how long the server must remember ReplayKey: until a
// request with this At could no longer be accepted, At + RequestSkew.
func (a HTTPAuth) RetainUntil() time.Time { return time.UnixMilli(a.At).Add(RequestSkew) }
