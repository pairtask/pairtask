package pairbook

import (
	"crypto/ed25519"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
)

// Changes are what people ask the owner node to fold into the next map:
// join (a new person, with the invite token's hash), bind or unbind one of
// their workspaces, or leave. Each is signed by the person's current key
// against the map it was made from (Base), and the owner applies it only
// while that map is still current: a compare-and-swap, so a replayed or
// stale request (a bind from before an unbind, say) never applies to a
// newer map.

const changeDomain = "pairbook-change-v2"

// Change actions.
const (
	ChangeJoin   = "join"
	ChangeBind   = "bind"
	ChangeUnbind = "unbind"
	ChangeLeave  = "leave"
)

// Change is one request. Join uses Name, TokenHash, Evidence and Binding;
// bind uses Binding; unbind uses Workspace; leave uses none of them.
type Change struct {
	Space     string
	Action    string
	Base      Mark
	Lineage   Lineage
	Name      string
	TokenHash [32]byte
	Evidence  PersonEvidence
	Binding   Binding
	Workspace string
	At        int64
}

// SignedChange is a change signed by the person's current key.
type SignedChange struct {
	Change
	Person ed25519.PublicKey
	Sig    []byte
}

// SigningBytes are the bytes a change's signature covers.
func (c Change) SigningBytes() []byte {
	e := newEncoder(changeDomain).str(c.Space).str(c.Action).count(c.Base.Version).bytes(c.Base.Digest[:]).bytes(c.Lineage[:])
	e.str(c.Name).bytes(c.TokenHash[:]).evidence(c.Evidence)
	return e.bytes(c.Binding.Claim).bytes(c.Binding.Acceptance).bytes(c.Binding.WorkspaceKey).str(c.Workspace).time(c.At).b
}

// SignChange signs c with the person's current key.
func SignChange(person ed25519.PrivateKey, c Change) SignedChange {
	return SignedChange{Change: c, Person: publicOf(person), Sig: sign(person, c.SigningBytes())}
}

// CheckChange checks a change against v, the owner's current map: same
// space, made from v (else ErrRollback), fresh at now, signed by the
// person's current key, and meaningful for the action. It doesn't check a
// join's token hash against the invites: the owner node does.
func (v VerifiedMap) CheckChange(s SignedChange, now time.Time) error {
	if s.Space != v.Space || !signedBy(s.Person, s.SigningBytes(), s.Sig) {
		return ErrInvalid
	}
	if s.Base != v.Mark {
		return ErrRollback
	}
	if Millis(now) >= v.ExpiresAt {
		return ErrExpired
	}
	if err := stamp(s.At).near(RequestSkew, now); err != nil {
		return err
	}
	if s.Action == ChangeJoin {
		return v.checkJoin(s, now)
	}
	return v.checkMember(s, now)
}

// checkJoin: a person not in the map, whose evidence names this key as
// current, binding the asking workspace.
func (v VerifiedMap) checkJoin(s SignedChange, now time.Time) error {
	if _, in := v.people[s.Lineage]; in || !validName(s.Name) {
		return ErrInvalid
	}
	st, err := s.Evidence.state(now)
	if err != nil || st.lineage != s.Lineage || st.person != KeyID(s.Person) {
		return ErrInvalid
	}
	if _, taken := v.workspaces[s.Binding.Workspace()]; taken {
		return ErrInvalid
	}
	return s.Binding.check(st, now)
}

// checkMember: a person in the map, under the key the map holds for them.
func (v VerifiedMap) checkMember(s SignedChange, now time.Time) error {
	p, in := v.people[s.Lineage]
	if !in || !sameKey(p.Person, s.Person) {
		return ErrInvalid
	}
	switch s.Action {
	case ChangeBind:
		return v.checkBind(s, p, now)
	case ChangeUnbind:
		if !slices.Contains(p.Workspaces, s.Workspace) {
			return ErrInvalid
		}
		return nil
	case ChangeLeave:
		if p.Role == RoleOwner {
			return ErrInvalid // an owner deletes the space instead
		}
		return nil
	}
	return ErrInvalid
}

func (v VerifiedMap) checkBind(s SignedChange, p Person, now time.Time) error {
	if _, taken := v.workspaces[s.Binding.Workspace()]; taken {
		return ErrInvalid
	}
	st := personState{lineage: p.Lineage, person: KeyID(p.Person), key: p.Person, epoch: p.Epoch}
	return s.Binding.check(st, now)
}

// claimedLineage is the lineage a binding's claim names.
func claimedLineage(b Binding) (Lineage, bool) {
	c, err := identity.ParseClaim(b.Claim)
	if err != nil {
		return Lineage{}, false
	}
	return Lineage(c.Lineage), true
}
