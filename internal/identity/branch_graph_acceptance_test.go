package identity_test

import (
	"crypto/ed25519"
	"slices"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
)

func branchChoice(t *testing.T, p wirePacket, h identity.Held, chosen identity.Digest) identity.Resolution {
	t.Helper()
	c := h.Conflicts[len(h.Conflicts)-1]
	var rejected []identity.Digest
	for _, b := range c.Branches {
		d := b[len(b)-1].Digest
		if d != chosen {
			rejected = append(rejected, d)
		}
	}
	slices.SortFunc(rejected, func(a, b identity.Digest) int { return slices.Compare(a[:], b[:]) })
	return identity.Resolution{Lineage: h.Lineage, Chosen: chosen, Rejected: rejected, BranchSet: identity.BranchSetDigest(c.Branches),
		At: time.UnixMilli(1700000000040)}.Signed(fixturePrivate(t, p, "W0"))
}

func interiorFork(t *testing.T, p wirePacket, nextRecovery string) identity.Handover {
	t.Helper()
	fork := parsedVector(t, p, "handover-2").(identity.Handover)
	fork.NextRecovery = ed25519.PublicKey(unhex(t, p.Fixtures[nextRecovery].Public))
	return fork.Signed(fixturePrivate(t, p, "R0"))
}

func TestIdentityBranchGraphAcceptanceInteriorForkInvalidatesDisplay(t *testing.T) {
	p := loadWirePacket(t)
	h := displayedConflict(t, p)
	r := parsedVector(t, p, "local-resolution").(identity.Resolution)
	next, _, err := h.Apply(interiorFork(t, p, "PLegacy"), time.UnixMilli(1700000010000))
	if err != nil {
		t.Fatal(err)
	}
	_, err = identity.Resolve(next, r, localPublic(t, p))
	if err == nil {
		t.Fatal("old confirmation accepted despite valid fork at interior predecessor")
	}
}

func TestIdentityBranchGraphAcceptanceThirdRecordInvalidatesDisplay(t *testing.T) {
	p := loadWirePacket(t)
	h := approvedAnchor(t, p)
	a := parsedVector(t, p, "record").(identity.Record)
	h = applyEffect(t, h, a, identity.Applied)
	b := a
	b.Bindings = nil
	b = b.Signed(fixturePrivate(t, p, "P0"))
	h = applyEffect(t, h, b, identity.FrozenEffect)
	r := branchChoice(t, p, h, identity.DigestOf(a.TBS()))
	c := a
	c.At = c.At.Add(time.Millisecond)
	c = c.Signed(fixturePrivate(t, p, "P0"))
	next, _, err := h.Apply(c, time.UnixMilli(1700000010000))
	if err != nil {
		t.Fatal(err)
	}
	_, err = identity.Resolve(next, r, localPublic(t, p))
	if err == nil {
		t.Fatal("old confirmation accepted despite third valid equivocal record")
	}
}

func TestIdentityBranchGraphAcceptanceSharedChosenPrefixIsNotRejected(t *testing.T) {
	p := loadWirePacket(t)
	h := displayedConflict(t, p)
	s1 := h.History[0]
	fork := interiorFork(t, p, "PLegacy")
	proof := identity.Proof{Statement: fork, Digest: identity.DigestOf(fork.TBS()), Predecessor: fork.Prev, Seq: fork.Seq}
	h.Conflicts[0].Branches = append(h.Conflicts[0].Branches, []identity.Proof{s1, proof})
	r := branchChoice(t, p, h, h.Head)
	h = resolveFixture(t, p, h, r)
	// S1 is in both the chosen and a rejected complete branch. A new valid
	// successor to S1 is a new conflict, not a descendant of rejected proof.
	h = applyEffect(t, h, interiorFork(t, p, "P2"), identity.FrozenEffect)
	if h.Trust != identity.Frozen {
		t.Fatal("shared chosen prefix was treated as rejected")
	}
}

func TestIdentityBranchGraphAcceptanceFutureRecordReconciledAfterCatchUp(t *testing.T) {
	p := loadWirePacket(t)
	h := approvedAnchor(t, p)
	s := parsedVector(t, p, "succession-1").(identity.Succession)
	r := identity.Record{Lineage: h.Lineage, Epoch: 2, Version: 7,
		Person: identity.KeyID(s.NextPerson), Recovery: h.Recovery, Counter: h.Counter,
		At: time.UnixMilli(1700000000040)}.Signed(fixturePrivate(t, p, "P1"))
	h = applyEffect(t, h, r, identity.Parked)
	h = applyEffect(t, h, s, identity.Applied)
	if h.RecordDigest != identity.DigestOf(r.TBS()) {
		t.Fatal("future-epoch record reported parked but was lost after recovery catch-up")
	}
	if h.Version != r.Version {
		t.Fatal("catch-up did not install authenticated future record version")
	}
}
