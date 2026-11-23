package pairbook

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"slices"
	"strings"
	"time"
)

// VerifiedMap is what a verified SpaceMap establishes. Use it only until
// ExpiresAt: Peers re-checks.
type VerifiedMap struct {
	Space     string
	Mark      Mark
	ExpiresAt int64

	org        bool // format 3: may hold org people
	people     map[Lineage]Person
	workspaces map[string]bound
	groups     map[string]map[Lineage]bool
	grants     []Grant
}

// Person is a verified member: lineage, current key, name, role, current
// epoch and the workspaces bound to them, sorted.
//
// An org person (Kind KindOrg) has a PersonID and no key or epoch; its
// Lineage is the map's own index for it, derived from the space and the
// PersonID, never a real lineage. Name people by Selector.
type Person struct {
	Lineage    Lineage
	Person     ed25519.PublicKey
	Name       string
	Role       string
	Epoch      uint64
	Workspaces []string
	Kind       string
	PersonID   PersonID
}

// Selector names p for status and policy: "org:<hex>" for an org person,
// the lineage hex for a key person.
func (p Person) Selector() string {
	if p.Kind == KindOrg {
		return p.PersonID.Selector()
	}
	return LineageHex(p.Lineage)
}

// Peer is a workspace a given workspace may connect to under the map.
type Peer struct {
	Workspace string
	Key       ed25519.PublicKey
	Lineage   Lineage
}

// bound is a workspace's owner in the map and its key.
type bound struct {
	lineage Lineage
	key     ed25519.PublicKey
}

// bind adds a binding: valid for its person, and the only binding of its
// workspace (one workspace never belongs to two people, nor twice).
func (v *VerifiedMap) bind(b Binding, states map[Lineage]personState, issued time.Time) error {
	l, err := v.bindingOwner(b, states, issued)
	if err != nil {
		return err
	}
	w := b.Workspace()
	if _, dup := v.workspaces[w]; dup {
		return ErrInvalid
	}
	v.workspaces[w] = bound{l, slices.Clone(b.WorkspaceKey)}
	p := v.people[l]
	p.Workspaces = append(p.Workspaces, w)
	slices.Sort(p.Workspaces)
	v.people[l] = p
	return nil
}

// bindingOwner checks b and is the key of the person it binds: by the
// person's claim for a key person, by the workspace's OrgProof for an org
// person (format 3).
func (v *VerifiedMap) bindingOwner(b Binding, states map[Lineage]personState, issued time.Time) (Lineage, error) {
	if b.Kind == KindOrg {
		return v.orgBindingOwner(b, issued)
	}
	if !b.keyOnly() {
		return Lineage{}, ErrInvalid
	}
	st, ok := bindingState(b, states)
	if !ok || b.check(st, issued) != nil {
		return Lineage{}, ErrInvalid
	}
	return st.lineage, nil
}

// orgBindingOwner: an org binding names an org person in the map, carries
// no person claim, and its workspace's proof is for this space, names the
// workspace, is signed by it and isn't dated after the map.
func (v *VerifiedMap) orgBindingOwner(b Binding, issued time.Time) (Lineage, error) {
	key := orgKey(v.Space, b.PersonID)
	if !v.org || !v.isPerson(key, KindOrg) {
		return Lineage{}, ErrInvalid
	}
	if len(b.Claim)+len(b.Acceptance) > 0 {
		return Lineage{}, ErrInvalid
	}
	if !v.proves(b, issued) {
		return Lineage{}, ErrInvalid
	}
	return key, nil
}

// proves: b's proof is its workspace's own, for this space, and not dated
// after the map by more than the clock skew a request may have: At is the
// machine's own clock, the map's IssuedAt the Pairbook's.
func (v *VerifiedMap) proves(b Binding, issued time.Time) bool {
	proof, err := ParseOrgProof(b.Proof)
	if err != nil {
		return false
	}
	return proof.Verify(v.Space, b.WorkspaceKey) == nil && proof.At <= Millis(issued.Add(RequestSkew))
}

// bindingState is the state of the person b's claim names, if in the map.
func bindingState(b Binding, states map[Lineage]personState) (personState, bool) {
	l, ok := claimedLineage(b)
	if !ok {
		return personState{}, false
	}
	st, known := states[l]
	return st, known
}

// People is every person in the map, sorted by lineage.
func (v VerifiedMap) People() []Person {
	out := make([]Person, 0, len(v.people))
	for _, p := range v.people {
		out = append(out, p.clone())
	}
	slices.SortFunc(out, func(a, b Person) int { return slices.Compare(a.Lineage[:], b.Lineage[:]) })
	return out
}

// Owner is the space's owner.
func (v VerifiedMap) Owner() Person {
	for _, p := range v.people {
		if p.Role == RoleOwner {
			return p.clone()
		}
	}
	return Person{}
}

// BoundTo is the person workspace is bound to.
func (v VerifiedMap) BoundTo(workspace string) (Person, bool) {
	b, ok := v.workspaces[workspace]
	if !ok {
		return Person{}, false
	}
	return v.people[b.lineage].clone(), true
}

// clone is p with its own key and workspace list, so a caller can't change
// the verified map through it.
func (p Person) clone() Person {
	p.Person = slices.Clone(p.Person)
	p.Workspaces = slices.Clone(p.Workspaces)
	return p
}

// Peers is the workspaces workspace may connect to at now: every other
// workspace of every person a grant pairs with workspace's person, sorted.
// None for a workspace the map doesn't bind; ErrExpired once the map has.
func (v VerifiedMap) Peers(workspace string, now time.Time) ([]Peer, error) {
	if Millis(now) >= v.ExpiresAt {
		return nil, ErrExpired
	}
	me, ok := v.workspaces[workspace]
	if !ok {
		return nil, nil
	}
	var out []Peer
	for w, b := range v.workspaces {
		if w != workspace && v.granted(me.lineage, b.lineage) {
			out = append(out, Peer{Workspace: w, Key: slices.Clone(b.key), Lineage: b.lineage})
		}
	}
	slices.SortFunc(out, func(a, b Peer) int { return strings.Compare(a.Workspace, b.Workspace) })
	return out, nil
}

// granted: some grant pairs a and b, either way round.
func (v VerifiedMap) granted(a, b Lineage) bool {
	for _, g := range v.grants {
		if v.pairs(g, a, b) || v.pairs(g, b, a) {
			return true
		}
	}
	return false
}

// pairs: g's A side selects a and its B side b.
func (v VerifiedMap) pairs(g Grant, a, b Lineage) bool {
	return v.selects(g.A, a) && v.selects(g.B, b)
}

func (v VerifiedMap) selects(s Selector, l Lineage) bool {
	switch s.Kind {
	case SelectAll:
		return true
	case SelectGroup:
		return v.groups[s.Name][l]
	}
	named, _, ok := v.selectedPerson(s)
	return ok && named == l
}

// LineageHex is l as a person selector names it.
func LineageHex(l Lineage) string { return hex.EncodeToString(l[:]) }

func lineageOf(s string) (Lineage, bool) {
	var l Lineage
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(l) {
		return l, false
	}
	copy(l[:], b)
	return l, true
}

// count appends n as a u64, for versions and list lengths.
func (e *encoder) count(n uint64) *encoder {
	e.b = binary.BigEndian.AppendUint64(e.b, n)
	return e
}

func (e *encoder) evidence(ev PersonEvidence) *encoder {
	e.bytes(ev.Anchor).count(uint64(len(ev.Statements)))
	for _, st := range ev.Statements {
		e.bytes(st)
	}
	e.count(uint64(len(ev.Workspaces)))
	for _, k := range ev.Workspaces {
		e.bytes(k)
	}
	return e
}

func (e *encoder) lineages(ls []Lineage) *encoder {
	e.count(uint64(len(ls)))
	for _, l := range ls {
		e.bytes(l[:])
	}
	return e
}
