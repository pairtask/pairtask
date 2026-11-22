package pairbook_test

import (
	"testing"
	"time"

	"github.com/pairtask/pairtask/pairbook"
)

func TestSpaceMapVersionAndExpiry(t *testing.T) {
	owner := contractKey("space owner")
	pin := pinnedID(public(owner))
	m := testMap()
	m.Space = "first"
	m.Version = 7
	original := pairbook.SignSpaceMap(owner, m)
	// Once a version is stored, expiry and restart must not reset that floor.
	// This is the verifier half. The store exercises restart through the client.
	for _, tc := range []struct {
		name        string
		version     uint64
		expiryDelta time.Duration
		want        bool
	}{
		{"identical map", 7, 0, true},
		{"same version with different signed bytes", 7, time.Second, false},
		{"older map freshly renewed", 6, time.Minute, false},
		{"next version", 8, 0, true},
		{"catch up across versions", 13, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := m
			next.Version = tc.version
			next.ExpiresAt += tc.expiryDelta.Milliseconds()
			s := pairbook.SignSpaceMap(owner, next)
			_, err := s.Verify("first", pin, original.Mark(), contractNow)
			if (err == nil) != tc.want {
				t.Fatalf("version check got %v, want accepted=%t", err, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name, space, owner string
		now                time.Time
	}{
		{"wrong space under same owner", "second", pin, contractNow},
		{"wrong owner", "first", pinnedID(public(contractKey("someone else"))), contractNow},
		{"before issue", "first", pin, time.UnixMilli(m.IssuedAt - 1)},
		{"at expiry", "first", pin, time.UnixMilli(m.ExpiresAt)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := original.Verify(tc.space, tc.owner, pairbook.Mark{}, tc.now)
			bad(t, err)
		})
	}
}

func TestPolicyPeersRequireGrantAndExpire(t *testing.T) {
	a, b, c := newBindingFixture("Alice"), newBindingFixture("Bob"), newBindingFixture("Chris")
	m := testMap()
	for _, f := range []*bindingFixture{&a, &b, &c} {
		f.workspace = contractKey(f.entry(pairbook.RoleMember).Name + pinnedID(public(f.person)))
		w := pinnedID(public(f.workspace))
		f.claim.Workspace = w
		f.accept.Workspace = w
		m.People = append(m.People, f.entry(pairbook.RoleMember))
		m.Bindings = append(m.Bindings, f.binding())
	}
	assertPeers := func(v pairbook.VerifiedMap, from string, want ...string) {
		t.Helper()
		ps, err := v.Peers(from, contractNow)
		good(t, err)
		if len(ps) != len(want) {
			t.Fatalf("%s: got %+v, want %v", from, ps, want)
		}
		for i, p := range ps {
			if p.Workspace != want[i] || pinnedID(p.Key) != p.Workspace {
				t.Fatalf("wrong peer %+v", p)
			}
		}
	}
	v, err := verifyMap(m)
	good(t, err)
	assertPeers(v, a.claim.Workspace)
	m.Groups = []pairbook.Group{{Name: "engineering", Members: []pairbook.Lineage{pairbook.Lineage(a.anchor.Lineage())}}}
	m.Grants = []pairbook.Grant{{A: pairbook.Selector{Kind: pairbook.SelectGroup, Name: "engineering"}, B: pairbook.Selector{Kind: pairbook.SelectPerson, Name: pairbook.LineageHex(pairbook.Lineage(b.anchor.Lineage()))}}}
	v, err = verifyMap(m)
	good(t, err)
	assertPeers(v, a.claim.Workspace, b.claim.Workspace)
	assertPeers(v, b.claim.Workspace, a.claim.Workspace)
	assertPeers(v, c.claim.Workspace)
	assertPeers(v, "unknown")
	_, err = v.Peers(a.claim.Workspace, time.UnixMilli(m.ExpiresAt))
	bad(t, err)
	m.Bindings[1].WorkspaceKey[0] ^= 1
	assertPeers(v, a.claim.Workspace, b.claim.Workspace)
}
