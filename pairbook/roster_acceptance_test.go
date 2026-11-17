package pairbook_test

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/pairtask/pairtask/pairbook"
)

func TestRevocationAcceptanceAuthorityAndTampering(t *testing.T) {
	owner, node, other := contractKey("owner"), contractKey("node"), contractKey("other")
	pin := pinnedID(public(owner))
	for _, by := range []ed25519.PrivateKey{owner, node} {
		r := pairbook.Revocation{Space: "s", Node: public(node), By: public(by), At: contractNow.UnixMilli()}
		signed := pairbook.SignRevocation(by, r)
		good(t, signed.Verify("s", pin))
		bad(t, signed.Verify("elsewhere", pin))
		bad(t, pairbook.SignRevocation(other, r).Verify("s", pin))
		for name, mutate := range map[string]func(*pairbook.SignedRevocation){
			"space": func(s *pairbook.SignedRevocation) { s.Space = "other" },
			"node":  func(s *pairbook.SignedRevocation) { s.Node = public(other) },
			"by":    func(s *pairbook.SignedRevocation) { s.By = public(other) },
			"time":  func(s *pairbook.SignedRevocation) { s.At++ },
		} {
			t.Run(name, func(t *testing.T) { c := signed; mutate(&c); bad(t, c.Verify("s", pin)) })
		}
	}
	outsider := pairbook.Revocation{Space: "s", Node: public(node), By: public(other), At: contractNow.UnixMilli()}
	bad(t, pairbook.SignRevocation(other, outsider).Verify("s", pin))
}

func TestRosterAcceptanceFiltersUntrustedRecords(t *testing.T) {
	owner, node, other := contractKey("owner"), contractKey("node"), contractKey("other")
	pin := pinnedID(public(owner))
	current := memberFor(owner, node, "s")
	expired := memberFor(owner, other, "s").Member
	expired.IssuedAt = contractNow.Add(-time.Hour).UnixMilli()
	expired.ExpiresAt = contractNow.UnixMilli()
	roster := pairbook.Roster{Members: []pairbook.SignedMember{
		memberFor(other, other, "s"), memberFor(owner, other, "elsewhere"),
		pairbook.SignMember(owner, expired), current, current,
	}}
	got := roster.Verified("s", pin, contractNow)
	if len(got) != 1 || pinnedID(got[0].Node) != pinnedID(public(node)) {
		t.Fatalf("untrusted directory result: %+v", got)
	}
}

func TestRosterAcceptanceRevocationAuthority(t *testing.T) {
	owner, node, other := contractKey("owner"), contractKey("node"), contractKey("other")
	pin := pinnedID(public(owner))
	current := memberFor(owner, node, "s")
	roster := pairbook.Roster{Members: []pairbook.SignedMember{current}}
	rev := pairbook.Revocation{Space: "s", Node: public(node), By: public(owner), At: current.IssuedAt}
	for name, v := range map[string]pairbook.SignedRevocation{
		"owner": pairbook.SignRevocation(owner, rev),
		"self":  pairbook.SignRevocation(node, pairbook.Revocation{Space: "s", Node: public(node), By: public(node), At: rev.At}),
	} {
		t.Run(name, func(t *testing.T) {
			r := roster
			r.Revocations = []pairbook.SignedRevocation{v}
			if got := r.Verified("s", pin, contractNow); len(got) != 0 {
				t.Fatal("revoked member survived")
			}
		})
	}
	for name, v := range map[string]pairbook.SignedRevocation{
		"forged":      pairbook.SignRevocation(other, rev),
		"cross-space": pairbook.SignRevocation(owner, pairbook.Revocation{Space: "elsewhere", Node: rev.Node, By: rev.By, At: rev.At}),
	} {
		t.Run(name, func(t *testing.T) {
			r := roster
			r.Revocations = []pairbook.SignedRevocation{v}
			if got := r.Verified("s", pin, contractNow); len(got) != 1 {
				t.Fatal("untrusted revocation hid member")
			}
		})
	}
}

func TestRosterAcceptanceHonorsLaterOwnerReadmission(t *testing.T) {
	owner, node := contractKey("owner"), contractKey("node")
	pin := pinnedID(public(owner))
	current := memberFor(owner, node, "s")
	rev := pairbook.Revocation{Space: "s", Node: public(node), By: public(owner), At: current.IssuedAt}
	roster := pairbook.Roster{Members: []pairbook.SignedMember{current}}
	roster.Revocations = []pairbook.SignedRevocation{pairbook.SignRevocation(owner, rev)}
	admitted := current.Member
	admitted.IssuedAt++
	admitted.Name = "Readmitted"
	roster.Members = append(roster.Members, pairbook.SignMember(owner, admitted))
	got := roster.Verified("s", pin, contractNow.Add(time.Millisecond))
	if len(got) != 1 || got[0].Name != "Readmitted" {
		t.Fatalf("later owner admission lost: %+v", got)
	}
}
