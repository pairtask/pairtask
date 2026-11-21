package spaceowner_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/internal/personkey"
	"github.com/pairtask/pairtask/internal/spaceowner"
	"github.com/pairtask/pairtask/pairbook"
)

const spaceID = "0123456789abcdef0123456789abcdef"

var at = time.Date(2026, 11, 21, 21, 0, 0, 0, time.UTC)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type person struct {
	keys *personkey.Keys
	me   spaceowner.Me
	p    ed25519.PublicKey
	w    ed25519.PrivateKey
}

func newPerson(t *testing.T) person {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "keys")
	keys, err := personkey.Create(dir)
	must(t, err)
	_, _, err = personkey.CreateAnchor(dir, false, at.Add(-time.Hour))
	must(t, err)
	me, err := spaceowner.Load(keys)
	must(t, err)
	_, w, err := ed25519.GenerateKey(rand.Reader)
	must(t, err)
	return person{keys, me, keys.PersonPublic(), w}
}

func (p person) binding() pairbook.Binding {
	pub := p.w.Public().(ed25519.PublicKey)
	a := identity.Acceptance{Lineage: identity.Lineage(p.me.Lineage), Epoch: 1, Person: pairbook.KeyID(p.p), Workspace: pairbook.KeyID(pub), At: at}
	return pairbook.Binding{Claim: p.me.Claim(pub, 1, at), Acceptance: a.Signed(p.w).Marshal(), WorkspaceKey: pub}
}

func (p person) create(t *testing.T) pairbook.SignedSpaceMap {
	t.Helper()
	m, err := spaceowner.NewSpace(p.me, spaceowner.Genesis{Space: spaceID, Name: "Owner", Own: p.binding()}, at)
	must(t, err)
	return m
}

func TestSpaceOwnerRequiresAnchor(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	keys, err := personkey.Create(dir)
	must(t, err)
	_, err = spaceowner.Load(keys)
	if !errors.Is(err, personkey.ErrNoAnchor) {
		t.Fatalf("missing anchor error: %v", err)
	}
}

func TestSpaceOwnerUsesExistingHostIdentity(t *testing.T) {
	p := newPerson(t)
	m := p.create(t)
	v, err := m.Verify(spaceID, pairbook.KeyID(p.p), pairbook.Mark{}, at)
	must(t, err)
	if m.Version != 1 || m.ExpiresAt != at.Add(pairbook.MapValidity).UnixMilli() {
		t.Fatal("incorrect genesis version or expiry")
	}
	if len(v.People()) != 1 || v.Owner().Lineage != p.me.Lineage {
		t.Fatal("incorrect initial owner")
	}
	all := pairbook.Selector{Kind: pairbook.SelectAll}
	if len(m.Grants) != 1 || m.Grants[0] != (pairbook.Grant{A: all, B: all}) {
		t.Fatal("missing explicit default policy")
	}
	if _, ok := v.BoundTo(p.binding().Workspace()); !ok {
		t.Fatal("owner workspace unavailable")
	}
}

func TestOwnerRejectsMismatchedWorkspaceAcceptance(t *testing.T) {
	p, other := newPerson(t), newPerson(t)
	b := p.binding()
	b.Acceptance = other.binding().Acceptance
	if _, err := spaceowner.NewSpace(p.me, spaceowner.Genesis{Space: spaceID, Name: "owner", Own: b}, at); err == nil {
		t.Fatal("owner created a map with a mismatched workspace acceptance")
	}
}

func TestRenewExpiredMapFromDurableMark(t *testing.T) {
	p := newPerson(t)
	m := p.create(t)
	now := time.UnixMilli(m.ExpiresAt).Add(time.Hour)
	next, err := p.me.Renew(spaceowner.Current{Map: m, Mark: m.Mark()}, now)
	must(t, err)
	_, err = next.Verify(spaceID, pairbook.KeyID(p.p), m.Mark(), now)
	must(t, err)
	if next.Version != m.Version+1 {
		t.Fatal("incorrect renewed version")
	}
	if next.IssuedAt != now.UnixMilli() || next.ExpiresAt != now.Add(pairbook.MapValidity).UnixMilli() {
		t.Fatal("incorrect renewal version or lifetime")
	}
	restore := next.SpaceMap
	restore.Version, restore.IssuedAt, restore.ExpiresAt = m.Version, m.IssuedAt, m.ExpiresAt
	if !bytes.Equal(restore.SigningBytes(), m.SigningBytes()) {
		t.Fatal("renewal changed signed space contents")
	}
}

func TestRenewRequiresOwnerAndExactDurableMark(t *testing.T) {
	p, other := newPerson(t), newPerson(t)
	m := p.create(t)
	now := at.Add(pairbook.MapValidity)
	for _, tc := range []struct {
		name   string
		signer spaceowner.Me
		mark   pairbook.Mark
	}{
		{"another host", other.me, m.Mark()},
		{"unheld map", p.me, pairbook.Mark{}},
		{"same version different digest", p.me, pairbook.Mark{Version: m.Version}},
		{"older snapshot", p.me, pairbook.Mark{Version: m.Version + 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.signer.Renew(spaceowner.Current{Map: m, Mark: tc.mark}, now); err == nil {
				t.Fatal("untrusted renewal succeeded")
			}
		})
	}
}

func (p person) join(mark pairbook.Mark, token [32]byte) pairbook.SignedChange {
	return p.me.Change(spaceowner.Request{Action: pairbook.ChangeJoin, Space: spaceID, Base: mark, Name: "Member", Binding: p.binding(), TokenHash: token}, at)
}

func TestBatchConsumesOneInviteOnce(t *testing.T) {
	owner, a, b := newPerson(t), newPerson(t), newPerson(t)
	m := owner.create(t)
	base := m.Mark()
	token := [32]byte{1}
	batch := spaceowner.Batch{Changes: []pairbook.SignedChange{a.join(base, token), b.join(base, token)}, Invited: func(hash [32]byte) bool { return hash == token }, Approved: func(pairbook.SignedChange) bool { return true }}
	next, out, err := owner.me.Fold(spaceowner.Current{Map: m, Mark: base}, batch, at)
	must(t, err)
	if next == nil || next.Version != base.Version+1 {
		t.Fatal("expected one successor")
	}
	checkOutcomes(t, out, spaceowner.Applied, spaceowner.Rejected)
	v, err := next.Verify(spaceID, pairbook.KeyID(owner.p), base, at)
	must(t, err)
	if _, ok := v.BoundTo(a.binding().Workspace()); !ok {
		t.Fatal("first join missing")
	}
	if _, ok := v.BoundTo(b.binding().Workspace()); ok {
		t.Fatal("second join consumed same invite")
	}
	if m.Mark() != base {
		t.Fatal("fold mutated previous map")
	}
	replayed, again, err := owner.me.Fold(spaceowner.Current{Map: *next, Mark: next.Mark()}, batch, at)
	must(t, err)
	if replayed != nil {
		t.Fatal("stale requests changed map")
	}
	checkOutcomes(t, again, spaceowner.Stale, spaceowner.Stale)
}

func checkOutcomes(t *testing.T, out []spaceowner.Outcome, want ...string) {
	t.Helper()
	if len(out) != len(want) {
		t.Fatalf("got %d outcomes, want %d", len(out), len(want))
	}
	for i, o := range out {
		if o.Result != want[i] {
			t.Fatalf("change %d got %q, want %q", i, o.Result, want[i])
		}
	}
}

func (p person) foldWithoutSuccessor(t *testing.T, m pairbook.SignedSpaceMap, changes ...pairbook.SignedChange) []spaceowner.Outcome {
	t.Helper()
	current := spaceowner.Current{Map: m, Mark: m.Mark()}
	next, out, err := p.me.Fold(current, spaceowner.Batch{Changes: changes}, at)
	must(t, err)
	if next != nil {
		t.Fatal("batch unexpectedly produced a successor map")
	}
	return out
}

func TestCurrentBindReplayAndOwnerLeave(t *testing.T) {
	owner := newPerson(t)
	m := owner.create(t)
	bind := owner.me.Change(spaceowner.Request{Action: pairbook.ChangeBind, Space: spaceID, Base: m.Mark(), Binding: owner.binding()}, at)
	leave := owner.me.Change(spaceowner.Request{Action: pairbook.ChangeLeave, Space: spaceID, Base: m.Mark()}, at)
	out := owner.foldWithoutSuccessor(t, m, bind, leave)
	checkOutcomes(t, out, spaceowner.Unchanged, spaceowner.Rejected)
}

func TestSignedInviteBoundToSpaceOwnerAndMap(t *testing.T) {
	owner := newPerson(t)
	m := owner.create(t)
	token := [32]byte{3}
	inv, err := owner.me.Invite(spaceowner.Current{Map: m, Mark: m.Mark()}, token, at, time.Hour)
	must(t, err)
	must(t, inv.Verify(spaceID, pairbook.KeyID(owner.p), at))
	if inv.Base != m.Mark() || inv.TokenHash != token {
		t.Fatal("invite lost its map or token binding")
	}
	if err := inv.Verify(spaceID, pairbook.KeyID(owner.p), at.Add(time.Hour)); err == nil {
		t.Fatal("expired invite accepted")
	}
	inv.Base.Version++
	if err := inv.Verify(spaceID, pairbook.KeyID(owner.p), at); err == nil {
		t.Fatal("modified invite accepted")
	}
	for _, d := range []time.Duration{0, time.Nanosecond, pairbook.InviteValidity + time.Second} {
		if _, err := owner.me.Invite(spaceowner.Current{Map: m, Mark: m.Mark()}, token, at, d); err == nil {
			t.Fatalf("invalid invite duration %v accepted", d)
		}
	}
}

func TestBatchRefusesUnauthorizedNoOp(t *testing.T) {
	owner, other := newPerson(t), newPerson(t)
	m := owner.create(t)
	b := owner.binding()
	forged := owner.me.Change(spaceowner.Request{Action: pairbook.ChangeBind, Space: spaceID, Base: m.Mark(), Binding: b}, at)
	forged.Sig[0] ^= 1
	unrelated := other.me.Change(spaceowner.Request{Action: pairbook.ChangeBind, Space: spaceID, Base: m.Mark(), Binding: b}, at)
	out := owner.foldWithoutSuccessor(t, m, forged, unrelated)
	checkOutcomes(t, out, spaceowner.Rejected, spaceowner.Rejected)
}

func TestLeaveRemovesAllMemberReferencesFromSuccessor(t *testing.T) {
	owner, member := newPerson(t), newPerson(t)
	m := owner.create(t)
	l := member.me.Lineage
	m.People = append(m.People, pairbook.MapPerson{Lineage: l, Person: member.p, Name: "Member", Role: pairbook.RoleMember, Evidence: member.me.Evidence})
	m.Bindings = append(m.Bindings, member.binding())
	m.Groups = []pairbook.Group{{Name: "team", Members: []pairbook.Lineage{owner.me.Lineage, l}}}
	who := pairbook.Selector{Kind: pairbook.SelectPerson, Name: pairbook.LineageHex(l)}
	group := pairbook.Selector{Kind: pairbook.SelectGroup, Name: "team"}
	upper := pairbook.Selector{Kind: pairbook.SelectPerson, Name: strings.ToUpper(who.Name)}
	m.Grants = append(m.Grants, pairbook.Grant{A: who, B: group}, pairbook.Grant{A: group, B: upper})
	m = pairbook.SignSpaceMap(owner.keys.PersonKey(), m.SpaceMap)
	base := m.Mark()
	leave := member.me.Change(spaceowner.Request{Action: pairbook.ChangeLeave, Space: spaceID, Base: base}, at)
	next, out, err := owner.me.Fold(spaceowner.Current{Map: m, Mark: base}, spaceowner.Batch{Changes: []pairbook.SignedChange{leave, leave}}, at)
	must(t, err)
	checkOutcomes(t, out, spaceowner.Applied, spaceowner.Rejected)
	if next == nil {
		t.Fatal("missing successor after leave")
	}
	v, err := next.Verify(spaceID, owner.me.KeyID(), base, at)
	must(t, err)
	if len(v.People()) != 1 {
		t.Fatal("departed person retained")
	}
	if len(next.Grants) != 1 || len(next.Groups[0].Members) != 1 {
		t.Fatal("incomplete removal from policy or roster")
	}
	if _, ok := v.BoundTo(member.binding().Workspace()); ok {
		t.Fatal("departed workspace retained")
	}
	if m.Mark() != base {
		t.Fatal("leave modified prior signed map")
	}
}

func TestJoinWaitsForOwnerApproval(t *testing.T) {
	owner, member := newPerson(t), newPerson(t)
	m := owner.create(t)
	token := [32]byte{7}
	change := member.join(m.Mark(), token)
	batch := spaceowner.Batch{Changes: []pairbook.SignedChange{change}}
	next, out, err := owner.me.Fold(spaceowner.Current{Map: m, Mark: m.Mark()}, batch, at)
	must(t, err)
	checkOutcomes(t, out, spaceowner.Rejected)
	if next != nil {
		t.Fatal("missing invite still produced successor")
	}
	batch.Invited = func(hash [32]byte) bool { return hash == token }
	next, out, err = owner.me.Fold(spaceowner.Current{Map: m, Mark: m.Mark()}, batch, at)
	must(t, err)
	checkOutcomes(t, out, spaceowner.AwaitingApproval)
	if next != nil {
		t.Fatal("unapproved join produced successor")
	}
}
