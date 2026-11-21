package pairbook

import (
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"slices"
	"time"
)

// SpaceMap is a space's whole control state, signed by its owner: the
// people in it, the workspaces each bound, and the policy of who may
// connect to whom. It plays the part of a Tailscale netmap. The Pairbook
// stores and serves it; it can withhold a newer map, but it can't forge
// one or make a client go back to an older one.
//
// Rollback boundary: a client keeps the highest map it ever verified (its
// Mark) across restarts and past expiry, and refuses anything older, or a
// different map at the same version. A client that has never seen this
// space can still be given an older map that hasn't expired, so a removal
// the Pairbook withholds keeps authorising, at most until that map's
// ExpiresAt (MapValidity after it was issued).

// MapValidity bounds a map's ExpiresAt - IssuedAt. The owner node signs a
// fresh version before half of it has passed.
const MapValidity = 14 * 24 * time.Hour

const mapDomain = "pairbook-map-v2"

// ErrRollback: a map older than, or different at the same version as, the
// highest one already verified.
var ErrRollback = errors.New("pairbook: older or divergent space map")

// Selector kinds a grant names.
const (
	SelectAll    = "all"    // every person in the map
	SelectGroup  = "group"  // the members of the group Name
	SelectPerson = "person" // the person whose lineage is Name (hex)
)

// Selector picks people for one side of a grant.
type Selector struct{ Kind, Name string }

// Grant lets every person A selects connect with every person B selects,
// both ways. A map with no grants connects no one.
type Grant struct{ A, B Selector }

// Group is a named set of people in the map.
type Group struct {
	Name    string
	Members []Lineage
}

// MapPerson is one person in the map: their lineage, current person key,
// display name, role (one owner, whose key signs the map) and evidence.
type MapPerson struct {
	Lineage  Lineage
	Person   ed25519.PublicKey
	Name     string
	Role     string
	Evidence PersonEvidence
}

// SpaceMap is the owner's map of a space at Version.
type SpaceMap struct {
	Space     string
	Version   uint64
	IssuedAt  int64
	ExpiresAt int64
	People    []MapPerson
	Bindings  []Binding
	Groups    []Group
	Grants    []Grant
}

// SignedSpaceMap is a map and the owner's signature.
type SignedSpaceMap struct {
	SpaceMap
	Owner ed25519.PublicKey
	Sig   []byte
}

// Mark is the highest map a client has verified: what it keeps to refuse
// rollback. The zero Mark accepts any valid map.
type Mark struct {
	Version uint64
	Digest  [32]byte
}

// SigningBytes are the bytes a map's signature covers.
func (m SpaceMap) SigningBytes() []byte {
	e := newEncoder(mapDomain).str(m.Space).count(m.Version).time(m.IssuedAt).time(m.ExpiresAt)
	e.count(uint64(len(m.People)))
	for _, p := range m.People {
		e.bytes(p.Lineage[:]).bytes(p.Person).str(p.Name).str(p.Role).evidence(p.Evidence)
	}
	e.count(uint64(len(m.Bindings)))
	for _, b := range m.Bindings {
		e.bytes(b.Claim).bytes(b.Acceptance).bytes(b.WorkspaceKey)
	}
	e.count(uint64(len(m.Groups)))
	for _, g := range m.Groups {
		e.str(g.Name).lineages(g.Members)
	}
	e.count(uint64(len(m.Grants)))
	for _, g := range m.Grants {
		e.str(g.A.Kind).str(g.A.Name).str(g.B.Kind).str(g.B.Name)
	}
	return e.b
}

// follows: m may replace prev, being newer or the same map again.
func (m Mark) follows(prev Mark) bool {
	if m.Version != prev.Version {
		return m.Version > prev.Version
	}
	return m.Digest == prev.Digest
}

// Mark is this map's place: its version and the digest of its signed
// bytes.
func (m SpaceMap) Mark() Mark { return Mark{m.Version, sha256.Sum256(m.SigningBytes())} }

// SignSpaceMap signs m with the owner's person key.
func SignSpaceMap(owner ed25519.PrivateKey, m SpaceMap) SignedSpaceMap {
	return SignedSpaceMap{SpaceMap: m, Owner: publicOf(owner), Sig: sign(owner, m.SigningBytes())}
}

// Verify checks the map is space's, signed by the pinned owner key (ID
// ownerKeyID), valid at now, not older than or divergent from mark, and
// internally consistent; and returns what it establishes. Re-verifying the
// map mark names is idempotent.
func (s SignedSpaceMap) Verify(space, ownerKeyID string, mark Mark, now time.Time) (VerifiedMap, error) {
	if s.Space != space || !signedBy(s.Owner, s.SigningBytes(), s.Sig) {
		return VerifiedMap{}, ErrInvalid
	}
	if !ownerID(ownerKeyID).is(s.Owner) {
		return VerifiedMap{}, ErrNotOwner
	}
	if err := (period{s.IssuedAt, s.ExpiresAt}).check(MapValidity, now); err != nil {
		return VerifiedMap{}, err
	}
	own := s.SpaceMap.Mark()
	if !own.follows(mark) {
		return VerifiedMap{}, ErrRollback
	}
	return s.build(own)
}

// build checks the map's contents and indexes them.
func (s SignedSpaceMap) build(own Mark) (VerifiedMap, error) {
	issued := time.UnixMilli(s.IssuedAt)
	people, states, err := s.people(issued)
	if err != nil {
		return VerifiedMap{}, err
	}
	v := VerifiedMap{Space: s.Space, Mark: own, ExpiresAt: s.ExpiresAt, people: people, workspaces: map[string]bound{}}
	for _, b := range s.Bindings {
		if err := v.bind(b, states, issued); err != nil {
			return VerifiedMap{}, err
		}
	}
	if v.groups, err = groupsOf(s.Groups, people); err != nil {
		return VerifiedMap{}, err
	}
	if !grantsValid(s.Grants, people, v.groups) {
		return VerifiedMap{}, ErrInvalid
	}
	v.grants = slices.Clone(s.Grants)
	return v, nil
}

// people checks each person once: valid name and role, evidence whose
// lineage and current key the entry names, and exactly one owner, whose
// key signed the map.
func (s SignedSpaceMap) people(at time.Time) (map[Lineage]Person, map[Lineage]personState, error) {
	people, states := map[Lineage]Person{}, map[Lineage]personState{}
	owners := 0
	for _, p := range s.People {
		st, err := p.Evidence.state(at)
		if err != nil || !s.entryValid(p, st) {
			return nil, nil, ErrInvalid
		}
		if _, dup := people[p.Lineage]; dup {
			return nil, nil, ErrInvalid
		}
		if p.Role == RoleOwner {
			owners++
		}
		people[p.Lineage] = Person{Lineage: p.Lineage, Person: slices.Clone(p.Person), Name: p.Name, Role: p.Role, Epoch: st.epoch}
		states[p.Lineage] = st
	}
	if owners != 1 {
		return nil, nil, ErrInvalid
	}
	return people, states, nil
}

// entryValid: p's fields agree with its evidence, and the owner role sits
// on the key that signed the map.
func (s SignedSpaceMap) entryValid(p MapPerson, st personState) bool {
	if !validName(p.Name) || !st.names(p.Lineage, p.Person) {
		return false
	}
	switch p.Role {
	case RoleOwner:
		return sameKey(p.Person, s.Owner)
	case RoleMember:
		return !sameKey(p.Person, s.Owner)
	}
	return false
}

// groupsOf indexes the groups: unique names, known members.
func groupsOf(groups []Group, people map[Lineage]Person) (map[string]map[Lineage]bool, error) {
	out := map[string]map[Lineage]bool{}
	for _, g := range groups {
		if !validName(g.Name) || out[g.Name] != nil {
			return nil, ErrInvalid
		}
		members, err := groupMembers(g, people)
		if err != nil {
			return nil, err
		}
		out[g.Name] = members
	}
	return out, nil
}

// groupMembers is g's members as a set of its own: each a person in the
// map, none twice.
func groupMembers(g Group, people map[Lineage]Person) (map[Lineage]bool, error) {
	members := map[Lineage]bool{}
	for _, l := range g.Members {
		if _, ok := people[l]; !ok || members[l] {
			return nil, ErrInvalid
		}
		members[l] = true
	}
	return members, nil
}

// grantsValid: every selector is well formed and names something in the
// map.
func grantsValid(grants []Grant, people map[Lineage]Person, groups map[string]map[Lineage]bool) bool {
	for _, g := range grants {
		if !selectorValid(g.A, people, groups) || !selectorValid(g.B, people, groups) {
			return false
		}
	}
	return true
}

func selectorValid(s Selector, people map[Lineage]Person, groups map[string]map[Lineage]bool) bool {
	switch s.Kind {
	case SelectAll:
		return s.Name == ""
	case SelectGroup:
		return groups[s.Name] != nil
	case SelectPerson:
		l, ok := lineageOf(s.Name)
		_, known := people[l]
		return ok && known
	}
	return false
}
