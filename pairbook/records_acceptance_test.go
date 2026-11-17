package pairbook_test

import (
	"bytes"
	"crypto/ed25519"
	"math"
	"testing"
	"time"

	"github.com/pairtask/pairtask/pairbook"
)

func TestMemberAcceptanceOwnerAuthoritySpaceAndTampering(t *testing.T) {
	owner, node, stranger := contractKey("owner"), contractKey("node"), contractKey("stranger")
	pinned := pinnedID(public(owner))
	original := memberFor(owner, node, "office")
	good(t, original.Verify("office", pinned, contractNow))
	bad(t, original.Verify("another-space", pinned, contractNow))
	bad(t, original.Verify("office", pinnedID(public(stranger)), contractNow))
	bad(t, original.Verify("office", "", contractNow))
	bad(t, memberFor(stranger, node, "office").Verify("office", pinned, contractNow))
	ownerRecord := original.Member
	ownerRecord.Node, ownerRecord.Role = public(owner), pairbook.RoleOwner
	good(t, pairbook.SignMember(owner, ownerRecord).Verify("office", pinned, contractNow))
	ownerRecord.Node = public(node)
	bad(t, pairbook.SignMember(owner, ownerRecord).Verify("office", pinned, contractNow))
	changes := map[string]func(*pairbook.SignedMember){
		"space":  func(s *pairbook.SignedMember) { s.Space = "elsewhere" },
		"node":   func(s *pairbook.SignedMember) { s.Node = public(stranger) },
		"name":   func(s *pairbook.SignedMember) { s.Name = "Impostor" },
		"role":   func(s *pairbook.SignedMember) { s.Role = pairbook.RoleOwner },
		"issued": func(s *pairbook.SignedMember) { s.IssuedAt-- },
		"expiry": func(s *pairbook.SignedMember) { s.ExpiresAt-- },
		"owner":  func(s *pairbook.SignedMember) { s.Owner = public(stranger) },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) { s := original; change(&s); bad(t, s.Verify("office", pinned, contractNow)) })
	}
	for _, role := range []string{"", "admin", "OWNER"} {
		s := original.Member
		s.Role = role
		bad(t, pairbook.SignMember(owner, s).Verify("office", pinned, contractNow))
	}
}

func TestRecordAcceptanceValidityBoundaries(t *testing.T) {
	owner, node := contractKey("owner"), contractKey("node")
	cases := []struct {
		name   string
		max    time.Duration
		verify func(int64, int64, time.Time) error
	}{
		{"member", pairbook.MemberValidity, func(start, end int64, now time.Time) error {
			m := memberFor(owner, node, "s").Member
			m.IssuedAt, m.ExpiresAt = start, end
			return pairbook.SignMember(owner, m).Verify("s", pinnedID(public(owner)), now)
		}},
		{"invite", pairbook.InviteValidity, func(start, end int64, now time.Time) error {
			i := pairbook.Invite{Space: "s", TokenHash: [32]byte{1}, IssuedAt: start, ExpiresAt: end}
			return pairbook.SignInvite(owner, i).Verify("s", pinnedID(public(owner)), now)
		}},
		{"address", pairbook.AddressValidity, func(start, end int64, now time.Time) error {
			a := pairbook.Address{Space: "s", Node: public(node), Addrs: []string{"localhost:7445"}, IssuedAt: start, ExpiresAt: end}
			return pairbook.SignAddress(node, a).Verify("s", now)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, end := contractNow.UnixMilli(), contractNow.Add(c.max).UnixMilli()
			good(t, c.verify(start, end, contractNow))
			good(t, c.verify(start, end, time.UnixMilli(end-1)))
			bad(t, c.verify(start, end, time.UnixMilli(start-1)))
			bad(t, c.verify(start, end, time.UnixMilli(end)))
			bad(t, c.verify(start, end+1, contractNow))
			bad(t, c.verify(start, start, contractNow))
			bad(t, c.verify(start, start-1, contractNow))
			bad(t, c.verify(-1, end, contractNow))
			bad(t, c.verify(math.MinInt64, math.MaxInt64, contractNow))
		})
	}
}

func TestInviteAndJoinAcceptanceSignersAndBinding(t *testing.T) {
	owner, node, other := contractKey("owner"), contractKey("node"), contractKey("other")
	i := pairbook.Invite{Space: "s", TokenHash: [32]byte{9}, IssuedAt: contractNow.UnixMilli(), ExpiresAt: contractNow.Add(time.Hour).UnixMilli()}
	invite := pairbook.SignInvite(owner, i)
	good(t, invite.Verify("s", pinnedID(public(owner)), contractNow))
	bad(t, invite.Verify("wrong", pinnedID(public(owner)), contractNow))
	bad(t, pairbook.SignInvite(other, i).Verify("s", pinnedID(public(owner)), contractNow))
	invite.TokenHash[0] ^= 1
	bad(t, invite.Verify("s", pinnedID(public(owner)), contractNow))
	r := pairbook.JoinRequest{Space: "s", Node: public(node), Name: "Laptop", TokenHash: i.TokenHash, At: contractNow.UnixMilli()}
	join := pairbook.SignJoinRequest(node, r)
	good(t, join.Verify("s", contractNow))
	bad(t, join.Verify("wrong", contractNow))
	bad(t, pairbook.SignJoinRequest(other, r).Verify("s", contractNow))
	for _, delta := range []time.Duration{-pairbook.JoinFreshness, pairbook.JoinFreshness} {
		good(t, join.Verify("s", contractNow.Add(delta)))
	}
	for _, delta := range []time.Duration{-pairbook.JoinFreshness - time.Millisecond, pairbook.JoinFreshness + time.Millisecond} {
		bad(t, join.Verify("s", contractNow.Add(delta)))
	}
	changed := join
	changed.Name = "Changed"
	bad(t, changed.Verify("s", contractNow))
	changed = join
	changed.TokenHash[0] ^= 1
	bad(t, changed.Verify("s", contractNow))
	changed = join
	changed.At++
	bad(t, changed.Verify("s", contractNow))
}

func TestAddressAcceptanceOwnershipAndEndpoints(t *testing.T) {
	node, other := contractKey("node"), contractKey("other")
	a := pairbook.Address{Space: "s", Node: public(node), Addrs: []string{"[::1]:7445", "localhost:7445"}, IssuedAt: contractNow.UnixMilli(), ExpiresAt: contractNow.Add(time.Hour).UnixMilli()}
	signed := pairbook.SignAddress(node, a)
	good(t, signed.Verify("s", contractNow))
	bad(t, signed.Verify("other-space", contractNow))
	bad(t, pairbook.SignAddress(other, a).Verify("s", contractNow))
	signed.Addrs = []string{"attacker.example:7445"}
	bad(t, signed.Verify("s", contractNow))
	for _, endpoint := range []string{"localhost", "localhost:0", "localhost:65536", ":7445", "http://localhost:7445", "host:word"} {
		t.Run(endpoint, func(t *testing.T) {
			c := a
			c.Addrs = []string{endpoint}
			bad(t, pairbook.SignAddress(node, c).Verify("s", contractNow))
		})
	}
	a.Addrs = nil
	bad(t, pairbook.SignAddress(node, a).Verify("s", contractNow))
	a.Addrs = make([]string, pairbook.MaxAddresses+1)
	for i := range a.Addrs {
		a.Addrs[i] = "localhost:7445"
	}
	bad(t, pairbook.SignAddress(node, a).Verify("s", contractNow))
}

func TestRecordAcceptanceMalformedKeysAndSignaturesDoNotPanic(t *testing.T) {
	owner, node := contractKey("owner"), contractKey("node")
	for _, n := range []int{0, 1, 31, 33, 64} {
		t.Run("key-length-"+time.Duration(n).String(), func(t *testing.T) {
			s := memberFor(owner, node, "s")
			s.Owner = bytes.Repeat([]byte{1}, n)
			bad(t, s.Verify("s", pinnedID(public(owner)), contractNow))
			s = memberFor(owner, node, "s")
			s.Node = bytes.Repeat([]byte{1}, n)
			bad(t, s.Verify("s", pinnedID(public(owner)), contractNow))
			a := pairbook.SignAddress(node, pairbook.Address{Space: "s", Node: public(node), Addrs: []string{"host:7445"}, IssuedAt: contractNow.UnixMilli(), ExpiresAt: contractNow.Add(time.Hour).UnixMilli()})
			a.Node = bytes.Repeat([]byte{1}, n)
			bad(t, a.Verify("s", contractNow))
		})
	}
	for _, n := range []int{0, 1, ed25519.SignatureSize - 1, ed25519.SignatureSize + 1} {
		s := memberFor(owner, node, "s")
		s.Sig = make([]byte, n)
		bad(t, s.Verify("s", pinnedID(public(owner)), contractNow))
	}
}

func TestRecordAcceptanceDisplayNamesAndAddressSet(t *testing.T) {
	owner, node := contractKey("owner"), contractKey("node")
	for _, name := range []string{"", string([]byte{0xff}), "line\nbreak", string(bytes.Repeat([]byte{'x'}, pairbook.MaxName+1))} {
		t.Run("invalid-name-"+name, func(t *testing.T) {
			m := memberFor(owner, node, "s").Member
			m.Name = name
			bad(t, pairbook.SignMember(owner, m).Verify("s", pinnedID(public(owner)), contractNow))
			j := pairbook.JoinRequest{Space: "s", Node: public(node), Name: name, At: contractNow.UnixMilli()}
			bad(t, pairbook.SignJoinRequest(node, j).Verify("s", contractNow))
		})
	}
	for _, name := range []string{"Łódź", string(bytes.Repeat([]byte{'x'}, pairbook.MaxName))} {
		m := memberFor(owner, node, "s").Member
		m.Name = name
		good(t, pairbook.SignMember(owner, m).Verify("s", pinnedID(public(owner)), contractNow))
	}
	a := pairbook.Address{Space: "s", Node: public(node), IssuedAt: contractNow.UnixMilli(), ExpiresAt: contractNow.Add(time.Hour).UnixMilli()}
	for _, addrs := range [][]string{{"z:7", "a:7"}, {"a:7", "a:7"}} {
		a.Addrs = addrs
		bad(t, pairbook.SignAddress(node, a).Verify("s", contractNow))
	}
	a.Addrs = nil
	for i := 0; i < pairbook.MaxAddresses; i++ {
		a.Addrs = append(a.Addrs, string(rune('a'+i))+":7")
	}
	good(t, pairbook.SignAddress(node, a).Verify("s", contractNow))
	a.Addrs = append(a.Addrs, "z:7")
	bad(t, pairbook.SignAddress(node, a).Verify("s", contractNow))
}
