package pairbook_test

import (
	"testing"
	"time"

	"github.com/pairtask/pairtask/pairbook"
)

func TestPersonJoinProvesPersonAndWorkspace(t *testing.T) {
	f := newBindingFixture("joining person")
	b := pairbook.Binding{Claim: f.claim.Signed(f.person).Marshal(), Acceptance: f.accept.Signed(f.workspace).Marshal(), WorkspaceKey: public(f.workspace)}
	v, err := verifyMap(testMap())
	good(t, err)
	request := pairbook.Change{Action: pairbook.ChangeJoin, Base: v.Mark, Lineage: pairbook.Lineage(f.anchor.Lineage()), TokenHash: [32]byte{17}, Space: "space", Name: "Alice", At: contractNow.UnixMilli(), Evidence: pairbook.PersonEvidence{Anchor: f.anchor.Marshal()}, Binding: b}
	signed := pairbook.SignChange(f.person, request)
	good(t, v.CheckChange(signed, contractNow))
	other := request
	other.Space = "other"
	bad(t, v.CheckChange(pairbook.SignChange(f.person, other), contractNow))
	bad(t, v.CheckChange(signed, contractNow.Add(pairbook.JoinFreshness+time.Millisecond)))
	bad(t, v.CheckChange(pairbook.SignChange(f.owner, request), contractNow))

	replayed := signed
	replayed.TokenHash[0] ^= 1
	bad(t, v.CheckChange(replayed, contractNow))
	next := testMap()
	next.Version++
	fresh, err := verifyMap(next)
	good(t, err)
	bad(t, fresh.CheckChange(signed, contractNow))
	later := time.UnixMilli(testMap().ExpiresAt)
	request.At = later.UnixMilli()
	bad(t, v.CheckChange(pairbook.SignChange(f.person, request), later))
	request.At = contractNow.UnixMilli()
	for _, tc := range []struct {
		name string
		edit func(*pairbook.Change)
	}{
		{"missing identity anchor", func(j *pairbook.Change) { j.Evidence.Anchor = nil }},
		{"different workspace public key", func(j *pairbook.Change) { j.Binding.WorkspaceKey = public(f.owner) }},
		{"missing workspace acceptance", func(j *pairbook.Change) { j.Binding.Acceptance = nil }},
		{"invalid person claim", func(j *pairbook.Change) { j.Binding.Claim = f.claim.Signed(f.workspace).Marshal() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := request
			tc.edit(&j)
			bad(t, v.CheckChange(pairbook.SignChange(f.person, j), contractNow))
		})
	}
}
