package identity_test

import (
	"bytes"
	"crypto/ed25519"
	"reflect"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
)

// Construct the displayed conflict from independent signed fixtures, without
// depending on Apply's conflict discovery. This isolates Resolve's contract.
func displayedConflict(t *testing.T, p wirePacket) identity.Held {
	t.Helper()
	h := approvedAnchor(t, p)
	s1 := parsedVector(t, p, "succession-1").(identity.Succession)
	h2 := parsedVector(t, p, "handover-2").(identity.Handover)
	fork := parsedVector(t, p, "fork-at-anchor").(identity.Succession)
	proof := func(s identity.Statement, prev identity.Digest, seq uint64) identity.Proof {
		return identity.Proof{Statement: s, Digest: identity.DigestOf(s.TBS()), Predecessor: prev, Seq: seq}
	}
	p1, p2, pf := proof(s1, s1.Prev, s1.Seq), proof(h2, h2.Prev, h2.Seq), proof(fork, fork.Prev, fork.Seq)
	h.Trust, h.Epoch, h.Counter, h.Seq = identity.Frozen, 2, 2, 2
	h.Person, h.Recovery, h.Head = identity.KeyID(s1.NextPerson), identity.KeyID(h2.NextRecovery), p2.Digest
	h.Keys[h.Person], h.Keys[h.Recovery] = s1.NextPerson, h2.NextRecovery
	h.Persons = append(h.Persons, h.Person)
	h.Recoveries = append(h.Recoveries, h.Recovery)
	h.History = []identity.Proof{p1, p2}
	h.Conflicts = []identity.Conflict{{Predecessor: s1.Prev, Branches: [][]identity.Proof{{p1, p2}, {pf}}}}
	return h
}

func localPublic(t *testing.T, p wirePacket) ed25519.PublicKey {
	t.Helper()
	return ed25519.PublicKey(unhex(t, p.Fixtures["W0"].Public))
}

func resolveFixture(t *testing.T, p wirePacket, h identity.Held, r identity.Resolution) identity.Held {
	t.Helper()
	before := stateBytes(t, h)
	next, err := identity.Resolve(h, r, localPublic(t, p))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, stateBytes(t, h)) {
		t.Fatal("Resolve mutated its input")
	}
	if next.Trust != identity.Active {
		t.Fatal("local choice did not restore active trust")
	}
	if len(next.Conflicts) == 0 {
		t.Fatal("resolution erased conflict evidence")
	}
	if len(next.Resolutions) == 0 {
		t.Fatal("resolution not retained")
	}
	return next
}

func TestIdentityResolutionAcceptanceFrozenCurrentChoice(t *testing.T) {
	p := loadWirePacket(t)
	r := parsedVector(t, p, "local-resolution").(identity.Resolution)
	h := displayedConflict(t, p)
	next := resolveFixture(t, p, h, r)
	if next.Head != r.Chosen {
		t.Fatal("selected head differs")
	}
	if next.Person != h.Person {
		t.Fatal("selected person differs")
	}
	if next.Recovery != h.Recovery {
		t.Fatal("selected recovery differs")
	}
}

func TestIdentityResolutionAcceptanceFrozenAlternateChoice(t *testing.T) {
	p := loadWirePacket(t)
	r := parsedVector(t, p, "local-resolution").(identity.Resolution)
	h := displayedConflict(t, p)
	fork := parsedVector(t, p, "fork-at-anchor").(identity.Succession)
	choice := r
	choice.Rejected = []identity.Digest{r.Chosen}
	choice.Chosen = identity.DigestOf(fork.TBS())
	choice = choice.Signed(fixturePrivate(t, p, "W0"))
	next := resolveFixture(t, p, h, choice)
	if next.Head != choice.Chosen {
		t.Fatal("resolution did not adopt alternate head")
	}
	if next.Person != identity.KeyID(fork.NextPerson) {
		t.Fatal("resolution did not adopt PFork")
	}
	if next.Recovery != identity.KeyID(h.Anchor.Recovery) {
		t.Fatal("resolution kept R from rejected handover")
	}
	if next.Counter != 1 {
		t.Fatal("resolution kept counter from rejected handover")
	}
	if next.Seq != 1 {
		t.Fatal("resolution kept rejected recovery sequence")
	}
}

func TestIdentityResolutionAcceptanceAuthorityAndDisplayedSet(t *testing.T) {
	p := loadWirePacket(t)
	cases := []struct {
		name   string
		change func(*identity.Held, *identity.Resolution)
	}{
		{"remote-signer", func(h *identity.Held, r *identity.Resolution) { *r = r.Signed(fixturePrivate(t, p, "PLegacy")) }},
		{"missing-rejected-head", func(h *identity.Held, r *identity.Resolution) {
			r.Rejected = nil
			*r = r.Signed(fixturePrivate(t, p, "W0"))
		}},
		{"chosen-also-rejected", func(h *identity.Held, r *identity.Resolution) {
			r.Rejected = []identity.Digest{r.Chosen}
			*r = r.Signed(fixturePrivate(t, p, "W0"))
		}},
		{"stale-displayed-set", func(h *identity.Held, r *identity.Resolution) {
			r.BranchSet[0] ^= 1
			*r = r.Signed(fixturePrivate(t, p, "W0"))
		}},
		{"unlisted-chosen-head", func(h *identity.Held, r *identity.Resolution) {
			r.Chosen[0] ^= 1
			*r = r.Signed(fixturePrivate(t, p, "W0"))
		}},
		{"another-lineage", func(h *identity.Held, r *identity.Resolution) {
			r.Lineage[0] ^= 1
			*r = r.Signed(fixturePrivate(t, p, "W0"))
		}},
		{"not-frozen", func(h *identity.Held, r *identity.Resolution) { h.Trust = identity.Active }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := displayedConflict(t, p)
			r := parsedVector(t, p, "local-resolution").(identity.Resolution)
			tc.change(&h, &r)
			before := stateBytes(t, h)
			next, err := identity.Resolve(h, r, localPublic(t, p))
			if err == nil {
				t.Fatal("invalid local choice accepted")
			}
			if !bytes.Equal(before, stateBytes(t, next)) {
				t.Fatal("rejected choice changed held state")
			}
		})
	}
}

func TestIdentityResolutionAcceptanceRejectedReplay(t *testing.T) {
	p := loadWirePacket(t)
	r := parsedVector(t, p, "local-resolution").(identity.Resolution)
	h := resolveFixture(t, p, displayedConflict(t, p), r)
	fork := parsedVector(t, p, "fork-at-anchor").(identity.Succession)
	next, effect, err := h.Apply(fork, time.UnixMilli(1700000010000))
	if err != nil {
		t.Fatal(err)
	}
	if effect.Kind != identity.Rejected {
		t.Fatalf("rejected replay effect=%+v", effect)
	}
	if !reflect.DeepEqual(h, next) {
		t.Fatal("rejected replay changed resolved state")
	}
	// A previously unseen descendant of the rejected branch must not park or
	// reactivate that branch. It is signed by the R active on the rejected fork.
	child := identity.Succession{Lineage: h.Lineage, Seq: 2, Prev: identity.DigestOf(fork.TBS()), Epoch: 2,
		Person: identity.KeyID(fork.NextPerson), NextPerson: ed25519.PublicKey(unhex(t, p.Fixtures["PLegacy"].Public)),
		Recovery: identity.KeyID(h.Anchor.Recovery), Counter: 1, At: time.UnixMilli(1700000000010)}.Signed(fixturePrivate(t, p, "R0"))
	next, effect, err = h.Apply(child, time.UnixMilli(1700000010000))
	if err != nil {
		t.Fatal(err)
	}
	if effect.Kind != identity.Rejected {
		t.Fatalf("rejected descendant effect=%+v", effect)
	}
	if next.Trust != identity.Active {
		t.Fatal("rejected descendant refroze identity")
	}
	if next.Head != h.Head {
		t.Fatal("rejected descendant changed chosen head")
	}
}

func TestIdentityResolutionAcceptanceNewConflict(t *testing.T) {
	p := loadWirePacket(t)
	r := parsedVector(t, p, "local-resolution").(identity.Resolution)
	h := resolveFixture(t, p, displayedConflict(t, p), r)
	fork := parsedVector(t, p, "fork-at-anchor").(identity.Succession)
	// A different, newly signed successor of the accepted anchor is a new
	// conflict; the old local resolution must not suppress it.
	newFork := fork
	newFork.NextPerson = ed25519.PublicKey(unhex(t, p.Fixtures["P2"].Public))
	newFork.At = newFork.At.Add(time.Millisecond)
	newFork = newFork.Signed(fixturePrivate(t, p, "R0"))
	next := applyEffect(t, h, newFork, identity.FrozenEffect)
	if next.Trust != identity.Frozen {
		t.Fatal("new conflict did not freeze")
	}
}
