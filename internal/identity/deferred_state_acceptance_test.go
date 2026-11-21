package identity_test

import (
	"crypto/ed25519"
	"slices"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
)

func TestIdentityDeferredStateAcceptanceEachReachedEpochAppliesRevocations(t *testing.T) {
	p := loadWirePacket(t)
	h := approvedAnchor(t, p)
	v := parsedVector(t, p, "revocation").(identity.Revocation)
	v.Epoch = 2
	v = v.Signed(fixturePrivate(t, p, "P1"))
	h = applyEffect(t, h, v, identity.Parked)
	h = applyEffect(t, h, parsedVector(t, p, "succession-3"), identity.Parked)
	h = applyEffect(t, h, parsedVector(t, p, "handover-2"), identity.Parked)
	h = applyEffect(t, h, parsedVector(t, p, "succession-1"), identity.Applied)
	if h.Epoch != 3 {
		t.Fatal("full recovery catch-up did not reach epoch3")
	}
	if !slices.Contains(h.Revoked, v.Device) {
		t.Fatal("catch-up skipped deferred epoch2 revocation before advancing to epoch3")
	}
}

func TestIdentityDeferredStateAcceptanceAlternateResolutionPreservesFutureEvidence(t *testing.T) {
	p := loadWirePacket(t)
	h := displayedConflict(t, p)
	h.Trust = identity.Active
	h.Conflicts = nil
	r := identity.Record{Lineage: h.Lineage, Epoch: 3, Version: 7,
		Person:   identity.KeyID(ed25519.PublicKey(unhex(t, p.Fixtures["PLegacy"].Public))),
		Recovery: identity.KeyID(h.Anchor.Recovery), Counter: 1, At: time.UnixMilli(1700000000050)}.Signed(fixturePrivate(t, p, "PLegacy"))
	h = applyEffect(t, h, r, identity.Parked)
	fork := parsedVector(t, p, "fork-at-anchor").(identity.Succession)
	h = applyEffect(t, h, fork, identity.FrozenEffect)
	choice := branchChoice(t, p, h, identity.DigestOf(fork.TBS()))
	h = resolveFixture(t, p, h, choice)
	s := identity.Succession{Lineage: h.Lineage, Seq: 2, Prev: h.Head, Epoch: 2, Person: h.Person,
		NextPerson: ed25519.PublicKey(unhex(t, p.Fixtures["PLegacy"].Public)), Recovery: h.Recovery, Counter: h.Counter,
		At: time.UnixMilli(1700000000050)}.Signed(fixturePrivate(t, p, "R0"))
	h = applyEffect(t, h, s, identity.Applied)
	if h.RecordDigest != identity.DigestOf(r.TBS()) {
		t.Fatal("alternate resolution dropped future record before its valid recovery path arrived")
	}
}

func TestIdentityDeferredStateAcceptanceFrozenRecordRequiresCurrentRecovery(t *testing.T) {
	p := loadWirePacket(t)
	cases := []struct {
		name   string
		change func(*identity.Record)
	}{
		{"wrong-counter", func(r *identity.Record) { r.Counter++ }},
		{"wrong-key", func(r *identity.Record) {
			r.Recovery = identity.KeyID(ed25519.PublicKey(unhex(t, p.Fixtures["R1"].Public)))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := approvedAnchor(t, p)
			a := parsedVector(t, p, "record").(identity.Record)
			h = applyEffect(t, h, a, identity.Applied)
			b := a
			b.Bindings = nil
			b = b.Signed(fixturePrivate(t, p, "P0"))
			h = applyEffect(t, h, b, identity.FrozenEffect)
			bad := a
			tc.change(&bad)
			bad = bad.Signed(fixturePrivate(t, p, "P0"))
			next, effect, err := h.Apply(bad, time.UnixMilli(1700000010000))
			requireRejected(t, effect, err)
			if len(next.Conflicts[0].Branches) != 2 {
				t.Fatal("record with wrong recovery state entered displayed alternatives")
			}
		})
	}
}
