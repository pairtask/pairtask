package pairbook_test

import (
	"crypto/ed25519"

	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/pairbook"
)

type bindingFixture struct {
	owner, person, workspace ed25519.PrivateKey
	anchor                   identity.Anchor
	claim                    identity.Claim
	accept                   identity.Acceptance
}

func newBindingFixture(label string) bindingFixture {
	f := bindingFixture{owner: contractKey("space owner"), person: contractKey(label), workspace: contractKey("shared machine")}
	at := contractNow.Add(-time.Hour)
	f.anchor = identity.Anchor{Person: public(f.person), InitAt: at.Add(-time.Hour)}.Signed(f.person)
	w := pinnedID(public(f.workspace))
	f.claim = identity.Claim{Lineage: f.anchor.Lineage(), Epoch: 1, Workspace: w, At: at}
	f.accept = identity.Acceptance{Lineage: f.anchor.Lineage(), Epoch: 1, Person: pinnedID(public(f.person)), Workspace: w, At: at}
	return f
}

func (f bindingFixture) entry(role string) pairbook.MapPerson {
	return pairbook.MapPerson{Lineage: pairbook.Lineage(f.anchor.Lineage()), Person: public(f.person), Name: "Member", Role: role, Evidence: pairbook.PersonEvidence{Anchor: f.anchor.Marshal()}}
}

func (f bindingFixture) binding() pairbook.Binding {
	return pairbook.Binding{WorkspaceKey: public(f.workspace), Claim: f.claim.Signed(f.person).Marshal(), Acceptance: f.accept.Signed(f.workspace).Marshal()}
}

func testMap() pairbook.SpaceMap {
	f := newBindingFixture("space owner")
	return pairbook.SpaceMap{Space: "space", Version: 1, IssuedAt: contractNow.UnixMilli(), ExpiresAt: contractNow.Add(time.Hour).UnixMilli(), People: []pairbook.MapPerson{f.entry(pairbook.RoleOwner)}}
}

func verifyMap(m pairbook.SpaceMap) (pairbook.VerifiedMap, error) {
	owner := contractKey("space owner")
	return pairbook.SignSpaceMap(owner, m).Verify(m.Space, pinnedID(public(owner)), pairbook.Mark{}, contractNow)
}

func TestPersonWorkspaceBindingAgreement(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*bindingFixture)
		want   bool
	}{
		{"binding survives membership renewal", func(*bindingFixture) {}, true},
		{"acceptance names another person", func(f *bindingFixture) { f.accept.Person = pinnedID(public(f.owner)) }, false},
		{"workspace key differs from signed ID", func(f *bindingFixture) { f.workspace = contractKey("different machine") }, false},
		{"acceptance from different epoch", func(f *bindingFixture) { f.accept.Epoch++ }, false},
		{"both statements from wrong epoch", func(f *bindingFixture) { f.accept.Epoch++; f.claim.Epoch++ }, false},
		{"different claim and acceptance times", func(f *bindingFixture) { f.accept.At = f.accept.At.Add(time.Second) }, false},
		{"future binding", func(f *bindingFixture) { f.claim.At = contractNow.Add(time.Hour); f.accept.At = f.claim.At }, false},
		{"different lineages", func(f *bindingFixture) { f.accept.Lineage[0] ^= 1 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBindingFixture("person")
			tc.change(&f)
			m := testMap()
			m.People = append(m.People, f.entry(pairbook.RoleMember))
			m.Bindings = []pairbook.Binding{f.binding()}
			v, err := verifyMap(m)
			if !tc.want {
				bad(t, err)
				return
			}
			good(t, err)
			person, ok := v.BoundTo(f.claim.Workspace)
			if !ok || person.Lineage != pairbook.Lineage(f.anchor.Lineage()) {
				t.Fatal("verified workspace missing from its person")
			}
		})
	}
}

func TestTwoPeopleClaimingOneWorkspace(t *testing.T) {
	a, b := newBindingFixture("first"), newBindingFixture("second")
	for _, order := range [][]bindingFixture{{a, b}, {b, a}} {
		m := testMap()
		for _, f := range order {
			m.People = append(m.People, f.entry(pairbook.RoleMember))
			m.Bindings = append(m.Bindings, f.binding())
		}
		_, err := verifyMap(m)
		bad(t, err)
	}
}

func TestMapAcceptsPersonEvidenceWithBoundWorkspace(t *testing.T) {
	f := newBindingFixture("member with public projection")
	m := testMap()
	person := f.entry(pairbook.RoleMember)
	record := identity.Record{Lineage: f.anchor.Lineage(), Epoch: 1, Version: 1, Person: pinnedID(public(f.person)), At: f.claim.At,
		Bindings: []identity.Binding{{Claim: f.claim.Signed(f.person), Acceptance: f.accept.Signed(f.workspace)}}}
	person.Evidence.Statements = [][]byte{record.Signed(f.person).Marshal()}
	person.Evidence.Workspaces = []ed25519.PublicKey{public(f.workspace)}
	m.People = append(m.People, person)
	m.Bindings = []pairbook.Binding{f.binding()}
	_, err := verifyMap(m)
	good(t, err)
}
