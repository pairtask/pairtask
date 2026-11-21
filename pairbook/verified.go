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

	people     map[Lineage]Person
	workspaces map[string]bound
	groups     map[string]map[Lineage]bool
	grants     []Grant
}

// Person is a verified member: lineage, current key, name, role, current
// epoch and the workspaces bound to them, sorted.
type Person struct {
	Lineage    Lineage
	Person     ed25519.PublicKey
	Name       string
	Role       string
	Epoch      uint64
	Workspaces []string
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
	l, ok := claimedLineage(b)
	st, known := states[l]
	if !ok || !known || b.check(st, issued) != nil {
		return ErrInvalid
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
		if (v.selects(g.A, a) && v.selects(g.B, b)) || (v.selects(g.A, b) && v.selects(g.B, a)) {
			return true
		}
	}
	return false
}

func (v VerifiedMap) selects(s Selector, l Lineage) bool {
	switch s.Kind {
	case SelectAll:
		return true
	case SelectGroup:
		return v.groups[s.Name][l]
	case SelectPerson:
		named, ok := lineageOf(s.Name)
		return ok && named == l
	}
	return false
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
