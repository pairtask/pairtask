package pairbook_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/pairbook"
)

var (
	danaID = pairbook.PersonID{1, 2, 3}
	samID  = pairbook.PersonID{9, 8, 7}
)

func orgPerson(id pairbook.PersonID, name string) pairbook.MapPerson {
	return pairbook.MapPerson{Kind: pairbook.KindOrg, PersonID: id, Name: name, Role: pairbook.RoleMember}
}

func orgBinding(space string, id pairbook.PersonID, machine string, at time.Time) pairbook.Binding {
	w := contractKey(machine)
	proof := pairbook.SignOrgProof(w, pairbook.OrgProof{Space: space, Workspace: pinnedID(public(w)), Nonce: [16]byte{4}, At: at.UnixMilli()})
	return pairbook.Binding{Kind: pairbook.KindOrg, PersonID: id, WorkspaceKey: public(w), Proof: proof.Marshal()}
}

// orgMap is a format-3 map: the owner, two org people with a machine each,
// and a grant between them by PersonID.
func orgMap() pairbook.SpaceMap {
	m := testMap()
	m.Format = pairbook.MapFormatOrg
	at := contractNow.Add(-time.Minute)
	m.People = append(m.People, orgPerson(danaID, "Dana"), orgPerson(samID, "Sam"))
	m.Bindings = []pairbook.Binding{orgBinding(m.Space, danaID, "dana laptop", at), orgBinding(m.Space, samID, "sam desktop", at)}
	m.Grants = []pairbook.Grant{{A: pairbook.Selector{Kind: pairbook.SelectOrg, Name: danaID.String()}, B: pairbook.Selector{Kind: pairbook.SelectOrg, Name: samID.String()}}}
	return m
}

func TestOrgMapGrantsOrgPeople(t *testing.T) {
	v, err := verifyMap(orgMap())
	good(t, err)
	dana := pinnedID(public(contractKey("dana laptop")))
	peers, err := v.Peers(dana, contractNow)
	good(t, err)
	if len(peers) != 1 || peers[0].Workspace != pinnedID(public(contractKey("sam desktop"))) {
		t.Fatalf("Dana's machine reaches %v, want Sam's machine", peers)
	}
	p, ok := v.BoundTo(dana)
	if !ok {
		t.Fatal("Dana's machine is bound to no one")
	}
	if got, want := p.Selector(), "org:"+danaID.String(); got != want || p.Kind != pairbook.KindOrg {
		t.Fatalf("Dana's machine is bound to %+v", p)
	}
	if p.Lineage == (pairbook.Lineage{}) {
		t.Fatal("an org person is indexed by the zero lineage")
	}
}

func TestOrgGroupMembersPair(t *testing.T) {
	m := orgMap()
	m.Groups = []pairbook.Group{{Name: "platform", OrgMembers: []pairbook.PersonID{danaID, samID}}}
	m.Grants = []pairbook.Grant{{A: pairbook.Selector{Kind: pairbook.SelectGroup, Name: "platform"}, B: pairbook.Selector{Kind: pairbook.SelectGroup, Name: "platform"}}}
	v, err := verifyMap(m)
	good(t, err)
	peers, err := v.Peers(pinnedID(public(contractKey("sam desktop"))), contractNow)
	good(t, err)
	if len(peers) != 1 {
		t.Fatalf("Sam's machine reaches %v through the group, want Dana's", peers)
	}
}

func TestOrgMapRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*pairbook.SpaceMap)
	}{
		{"org person in a format-2 map", func(m *pairbook.SpaceMap) { m.Format = pairbook.MapFormatKey }},
		{"org person with a lineage", func(m *pairbook.SpaceMap) { m.People[1].Lineage = pairbook.Lineage{1} }},
		{"org person with a person key", func(m *pairbook.SpaceMap) { m.People[1].Person = public(contractKey("stray")) }},
		{"org person as owner", func(m *pairbook.SpaceMap) { m.People[1].Role = pairbook.RoleOwner }},
		{"zero person ID", func(m *pairbook.SpaceMap) { m.People[1].PersonID = pairbook.PersonID{} }},
		{"duplicate person ID", func(m *pairbook.SpaceMap) { m.People[2].PersonID = danaID }},
		{"unknown person kind", func(m *pairbook.SpaceMap) { m.People[1].Kind = "robot" }},
		{"binding to an unknown person", func(m *pairbook.SpaceMap) { m.Bindings[0].PersonID = pairbook.PersonID{5} }},
		{"binding with a person claim too", func(m *pairbook.SpaceMap) { m.Bindings[0].Claim = []byte("claim") }},
		{"proof for another space", func(m *pairbook.SpaceMap) {
			m.Bindings[0] = orgBinding("another space", danaID, "dana laptop", contractNow.Add(-time.Minute))
		}},
		{"proof by another machine", func(m *pairbook.SpaceMap) { m.Bindings[0].WorkspaceKey = public(contractKey("someone else")) }},
		{"proof dated after the map beyond clock skew", func(m *pairbook.SpaceMap) {
			m.Bindings[0] = orgBinding(m.Space, danaID, "dana laptop", contractNow.Add(pairbook.RequestSkew+time.Second))
		}},
		{"truncated proof", func(m *pairbook.SpaceMap) { m.Bindings[0].Proof = m.Bindings[0].Proof[:20] }},
		{"org selector naming no one", func(m *pairbook.SpaceMap) { m.Grants[0].A.Name = pairbook.PersonID{6}.String() }},
		{"person selector naming an org person", func(m *pairbook.SpaceMap) {
			m.Grants[0].A = pairbook.Selector{Kind: pairbook.SelectPerson, Name: danaID.String()}
		}},
		{"org members in a format-2 group", func(m *pairbook.SpaceMap) {
			m.Format, m.People, m.Bindings, m.Grants = pairbook.MapFormatKey, m.People[:1], nil, nil
			m.Groups = []pairbook.Group{{Name: "g", OrgMembers: []pairbook.PersonID{danaID}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := orgMap()
			tc.change(&m)
			bad(t, func() error { _, err := verifyMap(m); return err }())
		})
	}
}

func TestUnknownMapFormatNeedsNewerClient(t *testing.T) {
	m := testMap()
	m.Format = 4
	if _, err := verifyMap(m); !errors.Is(err, pairbook.ErrUnknownFormat) {
		t.Fatalf("got %v, want ErrUnknownFormat", err)
	}
}

// A format-3 signature never verifies as format 2: a client that reads it
// as an older map (dropping the new fields) fails closed.
func TestOrgMapSignatureIsFormatBound(t *testing.T) {
	owner := contractKey("space owner")
	signed := pairbook.SignSpaceMap(owner, orgMap())
	signed.Format = pairbook.MapFormatKey
	if _, err := signed.Verify(signed.Space, pinnedID(public(owner)), pairbook.Mark{}, contractNow); err == nil {
		t.Fatal("a format-3 map verified as format 2")
	}
}

// Format-2 maps keep their JSON: none of the new fields appear.
func TestKeyMapJSONUnchanged(t *testing.T) {
	m := testMap()
	m.Groups = []pairbook.Group{{Name: "g", Members: []pairbook.Lineage{m.People[0].Lineage}}}
	b, err := json.Marshal(pairbook.SignSpaceMap(contractKey("space owner"), m))
	good(t, err)
	for _, field := range []string{`"Format"`, `"Kind"`, `"PersonID"`, `"OrgMembers"`, `"Proof"`} {
		if strings.Contains(string(b), field) {
			t.Fatalf("a format-2 map's JSON carries %s", field)
		}
	}
}

func TestOrgProofRoundTrip(t *testing.T) {
	w := contractKey("machine")
	p := pairbook.OrgProof{Space: "space", Workspace: pinnedID(public(w)), Nonce: [16]byte{7}, At: contractNow.UnixMilli()}
	got, err := pairbook.ParseOrgProof(pairbook.SignOrgProof(w, p).Marshal())
	good(t, err)
	if got.OrgProof != p {
		t.Fatalf("parsed %+v, want %+v", got.OrgProof, p)
	}
	good(t, got.Verify("space", public(w)))
	bad(t, got.Verify("space", public(contractKey("other"))))
	marshalled := pairbook.SignOrgProof(w, p).Marshal()
	_, err = pairbook.ParseOrgProof(append([]byte{0}, marshalled...))
	bad(t, err)
}

// A binding names its person exactly as the person entry does, for both
// kinds, so a roster keyed by selector puts each machine under its person.
func TestBindingAndPersonSelectorsAgree(t *testing.T) {
	m := orgMap()
	for i, b := range m.Bindings {
		got, ok := b.Selector()
		if want := m.People[i+1].Selector(); !ok || got != want {
			t.Fatalf("org binding %d names %q, its person %q", i, got, want)
		}
	}
	f := newBindingFixture("space owner")
	got, ok := f.binding().Selector()
	if want := f.entry(pairbook.RoleOwner).Selector(); !ok || got != want {
		t.Fatalf("key binding names %q, its person %q", got, want)
	}
	if m.People[1].Selector() == m.People[2].Selector() {
		t.Fatal("two org people share a selector")
	}
}

// A machine whose clock runs a little ahead still binds: its proof may be
// dated after the map by up to the request skew.
func TestOrgProofWithinClockSkewBinds(t *testing.T) {
	m := orgMap()
	m.Bindings[0] = orgBinding(m.Space, danaID, "dana laptop", contractNow.Add(time.Minute))
	_, err := verifyMap(m)
	good(t, err)
}
