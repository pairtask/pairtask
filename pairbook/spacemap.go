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

// ErrUnknownFormat: a map of a format this client doesn't know; a newer
// pairtask reads it.
var ErrUnknownFormat = errors.New("pairbook: this space's map needs a newer pairtask")

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
	Name       string
	Members    []Lineage  // key people
	OrgMembers []PersonID `json:",omitzero"` // org people; format 3 only
}

// MapPerson is one person in the map: their lineage, current person key,
// display name, role (one owner, whose key signs the map) and evidence.
//
// An org person (Kind KindOrg, format 3 only) is vouched for by the map's
// own signature: it has a PersonID, a name and the member role, and no
// lineage, key or evidence.
type MapPerson struct {
	Lineage  Lineage
	Person   ed25519.PublicKey
	Name     string
	Role     string
	Evidence PersonEvidence
	Kind     string   `json:",omitzero"`
	PersonID PersonID `json:",omitzero"`
}

// SpaceMap is the owner's map of a space at Version.
type SpaceMap struct {
	Format    uint8 `json:",omitzero"` // MapFormatKey when zero
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

// SigningBytes are the bytes a map's signature covers: format 2's exactly
// as before, format 3's under their own domain.
func (m SpaceMap) SigningBytes() []byte {
	if m.Format == MapFormatOrg {
		return m.orgSigningBytes()
	}
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

// orgSigningBytes are a format-3 map's: every field of format 2, and the
// org fields beside them.
func (m SpaceMap) orgSigningBytes() []byte {
	e := newEncoder(mapDomainV3).str(m.Space).count(m.Version).time(m.IssuedAt).time(m.ExpiresAt)
	e.count(uint64(len(m.People)))
	for _, p := range m.People {
		e.str(p.Kind).bytes(p.PersonID[:]).bytes(p.Lineage[:]).bytes(p.Person).str(p.Name).str(p.Role).evidence(p.Evidence)
	}
	e.count(uint64(len(m.Bindings)))
	for _, b := range m.Bindings {
		e.str(b.Kind).bytes(b.PersonID[:]).bytes(b.Proof).bytes(b.Claim).bytes(b.Acceptance).bytes(b.WorkspaceKey)
	}
	e.count(uint64(len(m.Groups)))
	for _, g := range m.Groups {
		e.str(g.Name).lineages(g.Members).personIDs(g.OrgMembers)
	}
	e.count(uint64(len(m.Grants)))
	for _, g := range m.Grants {
		e.str(g.A.Kind).str(g.A.Name).str(g.B.Kind).str(g.B.Name)
	}
	return e.b
}

// knownFormat: a format this client verifies.
func (m SpaceMap) knownFormat() bool {
	return m.Format == 0 || m.Format == MapFormatKey || m.Format == MapFormatOrg
}

// org: the map may hold org people.
func (m SpaceMap) org() bool { return m.Format == MapFormatOrg }

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
	if !s.knownFormat() {
		return VerifiedMap{}, ErrUnknownFormat
	}
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
	v := VerifiedMap{Space: s.Space, Mark: own, ExpiresAt: s.ExpiresAt, org: s.org(), people: people, workspaces: map[string]bound{}}
	for _, b := range s.Bindings {
		if err := v.bind(b, states, issued); err != nil {
			return VerifiedMap{}, err
		}
	}
	if v.groups, err = v.groupsOf(s.Groups); err != nil {
		return VerifiedMap{}, err
	}
	if !v.grantsValid(s.Grants) {
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
		key, person, st, err := s.person(p, at)
		if err != nil {
			return nil, nil, err
		}
		if _, dup := people[key]; dup {
			return nil, nil, ErrInvalid
		}
		if p.Role == RoleOwner {
			owners++
		}
		people[key] = person
		if p.Kind == "" {
			states[key] = st
		}
	}
	if owners != 1 {
		return nil, nil, ErrInvalid
	}
	return people, states, nil
}

// person checks one entry and is the key the map indexes it by: its
// lineage for a key person, orgKey for an org person.
func (s SignedSpaceMap) person(p MapPerson, at time.Time) (Lineage, Person, personState, error) {
	switch {
	case p.Kind == KindOrg && s.org() && orgEntryValid(p):
		key := orgKey(s.Space, p.PersonID)
		return key, Person{Lineage: key, Name: p.Name, Role: p.Role, Kind: KindOrg, PersonID: p.PersonID}, personState{}, nil
	case p.Kind != "" || p.PersonID != PersonID{}:
		return Lineage{}, Person{}, personState{}, ErrInvalid
	}
	st, err := p.Evidence.state(at)
	if err != nil || !s.entryValid(p, st) {
		return Lineage{}, Person{}, personState{}, ErrInvalid
	}
	return p.Lineage, Person{Lineage: p.Lineage, Person: slices.Clone(p.Person), Name: p.Name, Role: p.Role, Epoch: st.epoch}, st, nil
}

// orgEntryValid: an org person has a PersonID, a valid name and the member
// role, and none of a key person's fields: a zero lineage never stands for
// anyone.
func orgEntryValid(p MapPerson) bool {
	if p.PersonID == (PersonID{}) || !validName(p.Name) || p.Role != RoleMember {
		return false
	}
	return p.Lineage == (Lineage{}) && len(p.Person) == 0 && p.Evidence.empty()
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
func (v VerifiedMap) groupsOf(groups []Group) (map[string]map[Lineage]bool, error) {
	out := map[string]map[Lineage]bool{}
	for _, g := range groups {
		if !validName(g.Name) || out[g.Name] != nil {
			return nil, ErrInvalid
		}
		members, err := v.groupMembers(g)
		if err != nil {
			return nil, err
		}
		out[g.Name] = members
	}
	return out, nil
}

// groupMembers is g's members as a set of its own: each a person in the
// map of the kind its list names, none twice. Org members need format 3.
func (v VerifiedMap) groupMembers(g Group) (map[Lineage]bool, error) {
	if len(g.OrgMembers) > 0 && !v.org {
		return nil, ErrInvalid
	}
	members := map[Lineage]bool{}
	for _, l := range g.Members {
		if !v.addMember(members, l, "") {
			return nil, ErrInvalid
		}
	}
	for _, id := range g.OrgMembers {
		if !v.addMember(members, orgKey(v.Space, id), KindOrg) {
			return nil, ErrInvalid
		}
	}
	return members, nil
}

// addMember adds key once, when it is a person of kind in the map.
func (v VerifiedMap) addMember(members map[Lineage]bool, key Lineage, kind string) bool {
	p, ok := v.people[key]
	if !ok || p.Kind != kind || members[key] {
		return false
	}
	members[key] = true
	return true
}

// grantsValid: every selector is well formed and names something in the
// map.
func (v VerifiedMap) grantsValid(grants []Grant) bool {
	for _, g := range grants {
		if !v.selectorValid(g.A) || !v.selectorValid(g.B) {
			return false
		}
	}
	return true
}

func (v VerifiedMap) selectorValid(s Selector) bool {
	switch s.Kind {
	case SelectAll:
		return s.Name == ""
	case SelectGroup:
		return v.groups[s.Name] != nil
	}
	key, kind, ok := v.selectedPerson(s)
	if !ok {
		return false
	}
	p, known := v.people[key]
	return known && p.Kind == kind
}

// selectedPerson is the key a person selector names and the kind it must
// be: a key person by lineage, or (format 3) an org person by PersonID.
func (v VerifiedMap) selectedPerson(s Selector) (Lineage, string, bool) {
	switch {
	case s.Kind == SelectPerson:
		l, ok := lineageOf(s.Name)
		return l, "", ok
	case s.Kind == SelectOrg && v.org:
		id, ok := personIDOf(s.Name)
		return orgKey(v.Space, id), KindOrg, ok
	}
	return Lineage{}, "", false
}
