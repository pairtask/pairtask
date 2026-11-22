package spaceowner_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/pairtask/pairtask/pairbook"
	"github.com/pairtask/pairtask/pairbook/owner"
)

func companyPolicy(t *testing.T, admin, removed, retained person) (pairbook.SignedSpaceMap, person, pairbook.Grant) {
	t.Helper()
	sm := admin.create(t).SpaceMap
	for _, p := range []person{removed, retained} {
		sm.People = append(sm.People, pairbook.MapPerson{Lineage: p.me.Lineage, Person: p.p, Name: "Engineer", Role: pairbook.RoleMember, Evidence: p.me.Evidence})
		sm.Bindings = append(sm.Bindings, p.binding())
	}
	second := removed
	_, key, err := ed25519.GenerateKey(rand.Reader)
	must(t, err)
	second.w = key
	sm.Bindings = append(sm.Bindings, second.binding())
	sm.Groups = []pairbook.Group{{Name: "Team", Members: []pairbook.Lineage{removed.me.Lineage, retained.me.Lineage}}}
	who := func(p person) pairbook.Selector {
		return pairbook.Selector{Kind: pairbook.SelectPerson, Name: pairbook.LineageHex(p.me.Lineage)}
	}
	team := pairbook.Selector{Kind: pairbook.SelectGroup, Name: "Team"}
	keep := pairbook.Grant{A: who(admin), B: team}
	sm.Grants = []pairbook.Grant{keep, {A: who(removed), B: who(retained)}, {A: who(admin), B: who(removed)}}
	return pairbook.SignSpaceMap(admin.keys.PersonKey(), sm), second, keep
}

func TestCompanyOwnerRemovesPersonFromSignedPolicy(t *testing.T) {
	admin, removed, retained := newPerson(t), newPerson(t), newPerson(t)
	previous, second, keep := companyPolicy(t, admin, removed, retained)
	oldMark := previous.Mark()
	current := owner.Current{Map: previous, Mark: oldMark}
	now := at.Add(time.Minute)
	before, err := previous.Verify(spaceID, admin.me.KeyID(), oldMark, now)
	must(t, err)
	next, err := admin.me.Remove(current, removed.me.Lineage, now)
	must(t, err)
	after, err := next.Verify(spaceID, admin.me.KeyID(), oldMark, now)
	must(t, err)
	if next.Version != previous.Version+1 || previous.Mark() != oldMark {
		t.Fatal("removal must create exactly one successor and preserve the input map")
	}
	if _, err := previous.Verify(spaceID, admin.me.KeyID(), next.Mark(), now); !errors.Is(err, pairbook.ErrRollback) {
		t.Fatal("cached old map accepted after removal", err)
	}
	checkRemovedWorkspaces(t, after, now, []person{removed, second})
	if !reflect.DeepEqual(next.Grants, []pairbook.Grant{keep}) {
		t.Fatal("unexpected grants after removal")
	}
	wantGroups := []pairbook.Group{{Name: "Team", Members: []pairbook.Lineage{retained.me.Lineage}}}
	if !reflect.DeepEqual(next.Groups, wantGroups) {
		t.Fatal("unexpected group after removal")
	}
	for _, p := range []person{admin, retained} {
		key := p.binding().Workspace()
		prior, _ := before.BoundTo(key)
		got, _ := after.BoundTo(key)
		if !reflect.DeepEqual(prior, got) {
			t.Fatal("surviving member changed")
		}
		checkRetainedPeer(t, after, key, now)
	}
}

func checkRetainedPeer(t *testing.T, after pairbook.VerifiedMap, key string, now time.Time) {
	t.Helper()
	peers, err := after.Peers(key, now)
	must(t, err)
	if len(peers) != 1 {
		t.Fatal("surviving connection policy changed")
	}

}

func checkRemovedWorkspaces(t *testing.T, after pairbook.VerifiedMap, now time.Time, workspaces []person) {
	t.Helper()
	for _, p := range workspaces {
		key := p.binding().Workspace()
		if _, ok := after.BoundTo(key); ok {
			t.Fatal("removed workspace still bound")
		}
		peers, err := after.Peers(key, now)
		must(t, err)
		if len(peers) != 0 {
			t.Fatal("removed workspace still has peers")
		}
	}
}
