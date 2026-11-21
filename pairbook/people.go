package pairbook

import (
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
)

// People (Pairbook v1): a space's members are people, not machines. The
// owner signs one membership per person, naming the person's lineage and
// current person key. Each person binds the workspaces they can be reached
// at: the person's Claim and the workspace's Acceptance, both signed, as
// one Binding. Addresses stay per workspace. The directory still only
// stores and forwards: a membership counts only under the pinned owner
// key, a person's current key and epoch come from their own signed
// identity statements, and a binding only from both signatures.
//
// Every exported field is wire bytes or a plain key, so a module outside
// pairtask can build and check these records.

// Domains of the people records. v0's member and revocation records stay
// valid until the client moves to people.
const (
	membershipDomain       = "pairbook-membership-v2"
	personJoinDomain       = "pairbook-person-join-v2"
	unbindDomain           = "pairbook-unbind-v2"
	personRevocationDomain = "pairbook-person-revocation-v2"
)

// ErrNotImplemented marks the people checks until they are built.
var ErrNotImplemented = errors.New("pairbook: not implemented")

// Lineage names a person across key changes: SHA-256 of their identity
// anchor's signed bytes.
type Lineage [32]byte

// PersonEvidence is what establishes a person's current key and epoch:
// their marshalled identity anchor and the identity statements after it
// (records, successions, handovers), in order.
type PersonEvidence struct {
	Anchor     []byte
	Statements [][]byte
}

// Binding says workspace W belongs to a person: the person's signed claim
// and W's signed acceptance of the same lineage, epoch, workspace and time,
// with W's public key (its ID must be the W both name).
type Binding struct {
	Claim        []byte
	Acceptance   []byte
	WorkspaceKey ed25519.PublicKey
}

// NewBinding binds workspace to the person in lineage l at epoch, at.
func NewBinding(person, workspace ed25519.PrivateKey, l Lineage, epoch uint64, at time.Time) Binding {
	wpub := publicOf(workspace)
	w := KeyID(wpub)
	claim := identity.Claim{Lineage: identity.Lineage(l), Epoch: epoch, Workspace: w, At: at}.Signed(person)
	accept := identity.Acceptance{Lineage: identity.Lineage(l), Epoch: epoch, Person: KeyID(publicOf(person)), Workspace: w, At: at}.Signed(workspace)
	return Binding{Claim: claim.Marshal(), Acceptance: accept.Marshal(), WorkspaceKey: wpub}
}

// Membership is a person admitted to a space by its owner.
type Membership struct {
	Space     string
	Lineage   Lineage
	Person    ed25519.PublicKey
	Name      string
	Role      string
	IssuedAt  int64
	ExpiresAt int64
}

// SignedMembership is a membership and the owner's signature.
type SignedMembership struct {
	Membership
	Owner ed25519.PublicKey
	Sig   []byte
}

// SigningBytes are the bytes a membership's signature covers.
func (m Membership) SigningBytes() []byte {
	return newEncoder(membershipDomain).str(m.Space).bytes(m.Lineage[:]).bytes(m.Person).str(m.Name).str(m.Role).time(m.IssuedAt).time(m.ExpiresAt).b
}

// SignMembership signs m with the owner's person key.
func SignMembership(owner ed25519.PrivateKey, m Membership) SignedMembership {
	return SignedMembership{Membership: m, Owner: publicOf(owner), Sig: sign(owner, m.SigningBytes())}
}

// PersonJoin asks to join a space as a person, with the person's evidence
// and the binding of the workspace asking.
type PersonJoin struct {
	Space    string
	Evidence PersonEvidence
	Name     string
	At       int64
	Binding  Binding
}

// SignedPersonJoin is a join request signed by the person key.
type SignedPersonJoin struct {
	PersonJoin
	Person ed25519.PublicKey
	Sig    []byte
}

// SigningBytes are the bytes a join request's signature covers.
func (j PersonJoin) SigningBytes() []byte {
	e := newEncoder(personJoinDomain).str(j.Space).bytes(j.Evidence.Anchor)
	e.time(int64(len(j.Evidence.Statements)))
	for _, st := range j.Evidence.Statements {
		e.bytes(st)
	}
	return e.str(j.Name).time(j.At).bytes(j.Binding.Claim).bytes(j.Binding.Acceptance).bytes(j.Binding.WorkspaceKey).b
}

// SignPersonJoin signs j with the person key.
func SignPersonJoin(person ed25519.PrivateKey, j PersonJoin) SignedPersonJoin {
	return SignedPersonJoin{PersonJoin: j, Person: publicOf(person), Sig: sign(person, j.SigningBytes())}
}

// Verify checks the request belongs to space, is fresh at now, is signed by
// the person's current key under its evidence, and carries a valid binding.
func (s SignedPersonJoin) Verify(space string, now time.Time) error { return ErrNotImplemented }

// Unbind withdraws a workspace from the person in Lineage.
type Unbind struct {
	Space     string
	Lineage   Lineage
	Workspace string
	At        int64
}

// SignedUnbind is an unbind signed by the person key.
type SignedUnbind struct {
	Unbind
	Person ed25519.PublicKey
	Sig    []byte
}

// SigningBytes are the bytes an unbind's signature covers.
func (u Unbind) SigningBytes() []byte {
	return newEncoder(unbindDomain).str(u.Space).bytes(u.Lineage[:]).str(u.Workspace).time(u.At).b
}

// SignUnbind signs u with the person key.
func SignUnbind(person ed25519.PrivateKey, u Unbind) SignedUnbind {
	return SignedUnbind{Unbind: u, Person: publicOf(person), Sig: sign(person, u.SigningBytes())}
}

// PersonRevocation removes a person, and every workspace they bound, from
// a space: signed by the owner, or by the person leaving.
type PersonRevocation struct {
	Space   string
	Lineage Lineage
	By      ed25519.PublicKey
	At      int64
}

// SignedPersonRevocation is a person revocation signed by its By key.
type SignedPersonRevocation struct {
	PersonRevocation
	Sig []byte
}

// SigningBytes are the bytes a person revocation's signature covers.
func (r PersonRevocation) SigningBytes() []byte {
	return newEncoder(personRevocationDomain).str(r.Space).bytes(r.Lineage[:]).bytes(r.By).time(r.At).b
}

// SignPersonRevocation signs r with by, which must be r.By's private key.
func SignPersonRevocation(by ed25519.PrivateKey, r PersonRevocation) SignedPersonRevocation {
	return SignedPersonRevocation{PersonRevocation: r, Sig: sign(by, r.SigningBytes())}
}

// People is a space's people records as the directory returns them, not
// yet trusted.
type People struct {
	Memberships []SignedMembership
	Evidence    []PersonEvidence
	Bindings    []Binding
	Unbinds     []SignedUnbind
	Revocations []SignedPersonRevocation
}

// Person is a verified member: their membership, current epoch, the
// workspaces bound to them, and workspaces another person also claims
// (which count for nobody).
type Person struct {
	Membership Membership
	Epoch      uint64
	Workspaces []string
	Conflicts  []string
}

// Verified is the people a node may trust at now in space, under the owner
// person key with ID ownerKeyID.
func (p People) Verified(space, ownerKeyID string, now time.Time) []Person { return nil }

// Owner is the space owner as a verified person.
func (p People) Owner(space, ownerKeyID string, now time.Time) (Person, bool) {
	return Person{}, false
}

// BoundTo is the verified person workspace is bound to, if exactly one.
func (p People) BoundTo(space, ownerKeyID, workspace string, now time.Time) (Person, bool) {
	return Person{}, false
}
