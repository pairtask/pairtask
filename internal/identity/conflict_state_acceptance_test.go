package identity_test

import (
	"crypto/ed25519"
	"slices"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
)

func TestIdentityConflictStateAcceptancePermanentRevocationsSurviveResolution(t *testing.T) {
	p := loadWirePacket(t)
	h := displayedConflict(t, p)
	v := parsedVector(t, p, "revocation").(identity.Revocation)
	h.Revoked = []string{v.Device}
	r := parsedVector(t, p, "local-resolution").(identity.Resolution)
	next := resolveFixture(t, p, h, r)
	if !slices.Contains(next.Revoked, v.Device) {
		t.Fatal("resolving current branch erased permanent device revocation")
	}
}

func TestIdentityConflictStateAcceptanceCurrentBranchKeepsCurrentRecord(t *testing.T) {
	p := loadWirePacket(t)
	h := displayedConflict(t, p)
	h.Trust = identity.Active
	rec := identity.Record{Lineage: h.Lineage, Epoch: h.Epoch, Version: 5, Person: h.Person,
		Recovery: h.Recovery, Counter: h.Counter, At: time.UnixMilli(1700000000020)}.Signed(fixturePrivate(t, p, "P1"))
	h = applyEffect(t, h, rec, identity.Applied)
	h.Trust = identity.Frozen
	r := parsedVector(t, p, "local-resolution").(identity.Resolution)
	next := resolveFixture(t, p, h, r)
	if next.RecordDigest != h.RecordDigest {
		t.Fatal("choosing unchanged branch erased its current record")
	}
	if next.Version != 5 {
		t.Fatal("choosing unchanged branch reset record anti-replay version")
	}
}

func TestIdentityConflictStateAcceptanceUsedKeysStayRetiredAfterAlternateChoice(t *testing.T) {
	p := loadWirePacket(t)
	h := displayedConflict(t, p)
	r := parsedVector(t, p, "local-resolution").(identity.Resolution)
	fork := parsedVector(t, p, "fork-at-anchor").(identity.Succession)
	r.Rejected = []identity.Digest{r.Chosen}
	r.Chosen = identity.DigestOf(fork.TBS())
	r = r.Signed(fixturePrivate(t, p, "W0"))
	h = resolveFixture(t, p, h, r)
	reuse := identity.Succession{Lineage: h.Lineage, Seq: h.Seq + 1, Prev: h.Head, Epoch: h.Epoch, Person: h.Person,
		NextPerson: ed25519.PublicKey(unhex(t, p.Fixtures["P1"].Public)), Recovery: h.Recovery, Counter: h.Counter,
		At: time.UnixMilli(1700000000030)}.Signed(fixturePrivate(t, p, "R0"))
	next, effect, err := h.Apply(reuse, time.UnixMilli(1700000010000))
	requireRejected(t, effect, err)
	if next.Person != h.Person {
		t.Fatal("old retired P1 became current again")
	}
}

func TestIdentityConflictStateAcceptanceResolvedRecordReplayStaysRejected(t *testing.T) {
	p := loadWirePacket(t)
	h := approvedAnchor(t, p)
	a := parsedVector(t, p, "record").(identity.Record)
	h = applyEffect(t, h, a, identity.Applied)
	b := a
	b.Bindings = nil
	b = b.Signed(fixturePrivate(t, p, "P0"))
	h = applyEffect(t, h, b, identity.FrozenEffect)
	c := h.Conflicts[len(h.Conflicts)-1]
	r := identity.Resolution{Lineage: h.Lineage, Chosen: identity.DigestOf(a.TBS()), Rejected: []identity.Digest{identity.DigestOf(b.TBS())},
		BranchSet: identity.BranchSetDigest(c.Branches), At: time.UnixMilli(1700000000030)}.Signed(fixturePrivate(t, p, "W0"))
	h = resolveFixture(t, p, h, r)
	next, effect, err := h.Apply(b, time.UnixMilli(1700000010000))
	requireRejected(t, effect, err)
	if next.Trust != identity.Active {
		t.Fatal("rejected record replay refroze identity")
	}
}

func TestIdentityConflictStateAcceptanceInvalidBindingCannotCreateRecordConflict(t *testing.T) {
	p := loadWirePacket(t)
	h := approvedAnchor(t, p)
	r := parsedVector(t, p, "record").(identity.Record)
	h = applyEffect(t, h, r, identity.Applied)
	bad := parsedVector(t, p, "record").(identity.Record)
	bad.Bindings[0].Acceptance = bad.Bindings[0].Acceptance.Signed(fixturePrivate(t, p, "PLegacy"))
	bad = bad.Signed(fixturePrivate(t, p, "P0"))
	next, effect, err := h.Apply(bad, time.UnixMilli(1700000010000))
	requireRejected(t, effect, err)
	if next.Trust != identity.Active {
		t.Fatal("invalid workspace countersignature froze identity")
	}
}

func TestIdentityConflictStateAcceptanceQueuedConflictsAreReconciled(t *testing.T) {
	p := loadWirePacket(t)
	h := approvedAnchor(t, p)
	a := parsedVector(t, p, "handover-2").(identity.Handover)
	b := a
	b.NextRecovery = ed25519.PublicKey(unhex(t, p.Fixtures["PLegacy"].Public))
	b = b.Signed(fixturePrivate(t, p, "R0"))
	h = applyEffect(t, h, a, identity.Parked)
	h = applyEffect(t, h, b, identity.Parked)
	next, _, err := h.Apply(parsedVector(t, p, "succession-1"), time.UnixMilli(1700000010000))
	if err != nil {
		t.Fatal(err)
	}
	if next.Trust != identity.Frozen {
		t.Fatal("catch-up applied one queued successor and silently stranded its valid conflict")
	}
	if len(next.Conflicts) == 0 {
		t.Fatal("queued conflict evidence lost")
	}
}

func TestIdentityConflictStateAcceptanceBranchGrowthInvalidatesDisplayedChoice(t *testing.T) {
	p := loadWirePacket(t)
	h := displayedConflict(t, p)
	r := parsedVector(t, p, "local-resolution").(identity.Resolution)
	next, _, err := h.Apply(parsedVector(t, p, "succession-3"), time.UnixMilli(1700000010000))
	if err != nil {
		t.Fatal(err)
	}
	if next.Trust != identity.Frozen {
		t.Fatal("incoming branch extension advanced frozen trust")
	}
	_, err = identity.Resolve(next, r, localPublic(t, p))
	if err == nil {
		t.Fatal("stale displayed branch-set confirmation accepted after valid branch grew")
	}
}
