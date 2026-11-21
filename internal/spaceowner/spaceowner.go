// Package spaceowner is what a person's own host does with their person
// key for Pairbook spaces (#421 Pairbook v1): make a space's first map,
// invite, fold people's requests into the next map, renew a map, and sign
// the person's own requests. It is pure: the broker keeps and fetches the
// maps, the host CLI passes them in and publishes what comes out. The
// person key never leaves this process.
package spaceowner

import (
	"crypto/ed25519"
	"errors"
	"math"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/internal/personkey"
	"github.com/pairtask/pairtask/pairbook"
)

var (
	ErrNotOwner = errors.New("spaceowner: the map isn't this person's space")
	ErrOverflow = errors.New("spaceowner: the map's version can't go higher")
	ErrValidity = errors.New("spaceowner: invalid validity")
	ErrMark     = errors.New("spaceowner: the map isn't the last one verified")
)

// Me is the person on this host: their key, lineage and evidence.
type Me struct {
	key      ed25519.PrivateKey
	Lineage  pairbook.Lineage
	Evidence pairbook.PersonEvidence
}

// Load is the person whose keys and stored anchor keys holds.
func Load(keys *personkey.Keys) (Me, error) {
	anchor, err := keys.Anchor()
	if err != nil {
		return Me{}, err
	}
	a, err := identity.ParseAnchor(anchor)
	if err != nil {
		return Me{}, err
	}
	return Me{key: keys.PersonKey(), Lineage: pairbook.Lineage(a.Lineage()), Evidence: pairbook.PersonEvidence{Anchor: anchor}}, nil
}

// Public is the person key.
func (m Me) Public() ed25519.PublicKey { return m.key.Public().(ed25519.PublicKey) }

// KeyID is the person key's ID, as spaces pin their owner.
func (m Me) KeyID() string { return pairbook.KeyID(m.Public()) }

// Claim is the person's half of binding workspace w at epoch, at: the
// broker adds w's acceptance.
func (m Me) Claim(w ed25519.PublicKey, epoch uint64, at time.Time) []byte {
	c := identity.Claim{Lineage: identity.Lineage(m.Lineage), Epoch: epoch, Workspace: pairbook.KeyID(w), At: at}
	return c.Signed(m.key).Marshal()
}

// NewSpace is space's first map: this person as owner, named name, with
// their workspace binding own, and the grant all ↔ all.
func NewSpace(m Me, space, name string, own pairbook.Binding, now time.Time) (pairbook.SignedSpaceMap, error) {
	all := pairbook.Selector{Kind: pairbook.SelectAll}
	sm := pairbook.SpaceMap{Space: space, Version: 1,
		People:   []pairbook.MapPerson{{Lineage: m.Lineage, Person: m.Public(), Name: name, Role: pairbook.RoleOwner, Evidence: m.Evidence}},
		Bindings: []pairbook.Binding{own},
		Grants:   []pairbook.Grant{{A: all, B: all}}}
	return m.issue(sm, pairbook.Mark{}, now)
}

// issue signs sm as valid from now and checks the result as a client
// would, against mark.
func (m Me) issue(sm pairbook.SpaceMap, mark pairbook.Mark, now time.Time) (pairbook.SignedSpaceMap, error) {
	sm.IssuedAt = pairbook.Millis(now)
	sm.ExpiresAt = pairbook.Millis(now.Add(pairbook.MapValidity))
	signed := pairbook.SignSpaceMap(m.key, sm)
	if _, err := signed.Verify(sm.Space, m.KeyID(), mark, now); err != nil {
		return pairbook.SignedSpaceMap{}, err
	}
	return signed, nil
}

// current verifies cur as this person's map at now, made no earlier than
// mark.
func (m Me) current(cur pairbook.SignedSpaceMap, mark pairbook.Mark, now time.Time) (pairbook.VerifiedMap, error) {
	v, err := cur.Verify(cur.Space, m.KeyID(), mark, now)
	if errors.Is(err, pairbook.ErrNotOwner) {
		return v, ErrNotOwner
	}
	return v, err
}

// Invite invites one person to cur's space with a one-time token's hash,
// valid for d from now, to join from cur.
func (m Me) Invite(cur pairbook.SignedSpaceMap, mark pairbook.Mark, tokenHash [32]byte, now time.Time, d time.Duration) (pairbook.SignedMapInvite, error) {
	v, err := m.current(cur, mark, now)
	if err != nil {
		return pairbook.SignedMapInvite{}, err
	}
	if d <= 0 || d > pairbook.InviteValidity {
		return pairbook.SignedMapInvite{}, ErrValidity
	}
	i := pairbook.MapInvite{Space: cur.Space, Base: v.Mark, TokenHash: tokenHash, IssuedAt: pairbook.Millis(now), ExpiresAt: pairbook.Millis(now.Add(d))}
	signed := pairbook.SignMapInvite(m.key, i)
	if signed.Verify(cur.Space, m.KeyID(), now) != nil {
		return pairbook.SignedMapInvite{}, ErrValidity // e.g. under a millisecond
	}
	return signed, nil
}

// Renew is last again as the next version, valid from now. last must be
// exactly the map mark records; it may have lapsed, so it is checked as of
// when it was issued.
func (m Me) Renew(last pairbook.SignedSpaceMap, mark pairbook.Mark, now time.Time) (pairbook.SignedSpaceMap, error) {
	if last.SpaceMap.Mark() != mark {
		return pairbook.SignedSpaceMap{}, ErrMark
	}
	if _, err := m.current(last, mark, time.UnixMilli(last.IssuedAt)); err != nil {
		return pairbook.SignedSpaceMap{}, err
	}
	return m.next(last.SpaceMap, mark, now)
}

// next is sm as the following version, signed and checked.
func (m Me) next(sm pairbook.SpaceMap, mark pairbook.Mark, now time.Time) (pairbook.SignedSpaceMap, error) {
	if sm.Version == math.MaxUint64 {
		return pairbook.SignedSpaceMap{}, ErrOverflow
	}
	sm.Version++
	return m.issue(sm, mark, now)
}

// ChangeArgs are what a request carries besides its action: a join's name,
// token hash and binding, a bind's binding, an unbind's workspace.
type ChangeArgs struct {
	Name      string
	TokenHash [32]byte
	Binding   pairbook.Binding
	Workspace string
}

// Change is this person's signed request to space, made from base.
func (m Me) Change(action, space string, base pairbook.Mark, args ChangeArgs, now time.Time) pairbook.SignedChange {
	c := pairbook.Change{Space: space, Action: action, Base: base, Lineage: m.Lineage, Name: args.Name, TokenHash: args.TokenHash,
		Binding: args.Binding, Workspace: args.Workspace, At: pairbook.Millis(now)}
	if action == pairbook.ChangeJoin {
		c.Evidence = m.Evidence
	}
	return pairbook.SignChange(m.key, c)
}
