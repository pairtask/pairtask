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

// Current is the map a space is at, as the broker keeps it: the last map
// verified and its durable mark.
type Current struct {
	Map  pairbook.SignedSpaceMap
	Mark pairbook.Mark
}

// Genesis is what a new space starts with: its ID, the owner's display
// name, and the owner's binding of their workspace.
type Genesis struct {
	Space string
	Name  string
	Own   pairbook.Binding
}

// NewSpace is a space's first map: this person as owner, with their
// workspace, and the grant all ↔ all.
func NewSpace(m Me, g Genesis, now time.Time) (pairbook.SignedSpaceMap, error) {
	all := pairbook.Selector{Kind: pairbook.SelectAll}
	sm := pairbook.SpaceMap{Space: g.Space, Version: 1,
		People:   []pairbook.MapPerson{{Lineage: m.Lineage, Person: m.Public(), Name: g.Name, Role: pairbook.RoleOwner, Evidence: m.Evidence}},
		Bindings: []pairbook.Binding{g.Own},
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

// current verifies c's map as this person's at now: exactly the map its
// mark records.
func (m Me) current(c Current, now time.Time) (pairbook.VerifiedMap, error) {
	if c.Map.SpaceMap.Mark() != c.Mark {
		return pairbook.VerifiedMap{}, ErrMark
	}
	v, err := c.Map.Verify(c.Map.Space, m.KeyID(), c.Mark, now)
	if errors.Is(err, pairbook.ErrNotOwner) {
		return v, ErrNotOwner
	}
	return v, err
}

// Invite invites one person to c's space with a one-time token's hash,
// valid for d from now, to join from c.
func (m Me) Invite(c Current, tokenHash [32]byte, now time.Time, d time.Duration) (pairbook.SignedMapInvite, error) {
	v, err := m.current(c, now)
	if err != nil {
		return pairbook.SignedMapInvite{}, err
	}
	if d <= 0 || d > pairbook.InviteValidity {
		return pairbook.SignedMapInvite{}, ErrValidity
	}
	i := pairbook.MapInvite{Space: c.Map.Space, Base: v.Mark, TokenHash: tokenHash, IssuedAt: pairbook.Millis(now), ExpiresAt: pairbook.Millis(now.Add(d))}
	signed := pairbook.SignMapInvite(m.key, i)
	if signed.Verify(c.Map.Space, m.KeyID(), now) != nil {
		return pairbook.SignedMapInvite{}, ErrValidity // e.g. under a millisecond
	}
	return signed, nil
}

// Renew is c's map again as the next version, valid from now. The map
// must be exactly the one c's mark records; it may have lapsed, so it is
// checked as of when it was issued.
func (m Me) Renew(c Current, now time.Time) (pairbook.SignedSpaceMap, error) {
	if _, err := m.current(c, time.UnixMilli(c.Map.IssuedAt)); err != nil {
		return pairbook.SignedSpaceMap{}, err
	}
	return m.next(c.Map.SpaceMap, c.Mark, now)
}

// next is sm as the following version, signed and checked.
func (m Me) next(sm pairbook.SpaceMap, mark pairbook.Mark, now time.Time) (pairbook.SignedSpaceMap, error) {
	if sm.Version == math.MaxUint64 {
		return pairbook.SignedSpaceMap{}, ErrOverflow
	}
	sm.Version++
	return m.issue(sm, mark, now)
}

// Request is a request this person makes to a space, from the map Base:
// a join's name, token hash and binding, a bind's binding, an unbind's
// workspace.
type Request struct {
	Space     string
	Action    string
	Base      pairbook.Mark
	Name      string
	TokenHash [32]byte
	Binding   pairbook.Binding
	Workspace string
}

// Change is r, signed by this person.
func (m Me) Change(r Request, now time.Time) pairbook.SignedChange {
	c := pairbook.Change{Space: r.Space, Action: r.Action, Base: r.Base, Lineage: m.Lineage, Name: r.Name, TokenHash: r.TokenHash,
		Binding: r.Binding, Workspace: r.Workspace, At: pairbook.Millis(now)}
	if r.Action == pairbook.ChangeJoin {
		c.Evidence = m.Evidence
	}
	return pairbook.SignChange(m.key, c)
}
