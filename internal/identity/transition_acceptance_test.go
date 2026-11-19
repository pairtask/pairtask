package identity_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
)

func approvedAnchor(t *testing.T, p wirePacket) identity.Held {
	t.Helper()
	a := parsedVector(t, p, "anchor").(identity.Anchor)
	person, recovery := identity.KeyID(a.Person), identity.KeyID(a.Recovery)
	w := ed25519.PublicKey(unhex(t, p.Fixtures["W0"].Public))
	return identity.Held{
		Lineage: a.Lineage(), Anchor: a, Trust: identity.Active,
		Epoch: 1, Counter: 1, Person: person, Recovery: recovery,
		Head:       identity.DigestOf(a.TBS()),
		Keys:       map[string]ed25519.PublicKey{person: a.Person, recovery: a.Recovery},
		Workspaces: map[string]ed25519.PublicKey{identity.KeyID(w): w},
		Persons:    []string{person}, Recoveries: []string{recovery},
	}
}

func stateBytes(t *testing.T, h identity.Held) []byte {
	t.Helper()
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func applyEffect(t *testing.T, h identity.Held, st identity.Statement, want identity.EffectKind) identity.Held {
	t.Helper()
	before := stateBytes(t, h)
	next, effect, err := h.Apply(st, time.UnixMilli(1700000010000))
	if err != nil {
		t.Fatal(err)
	}
	if effect.Kind != want {
		t.Fatalf("effect=%+v, want %s", effect, want)
	}
	if !bytes.Equal(before, stateBytes(t, h)) {
		t.Fatal("Apply mutated its input state or aliased collections")
	}
	return next
}

func chainThroughHandover(t *testing.T, p wirePacket) identity.Held {
	t.Helper()
	h := approvedAnchor(t, p)
	h = applyEffect(t, h, parsedVector(t, p, "succession-1"), identity.Applied)
	return applyEffect(t, h, parsedVector(t, p, "handover-2"), identity.Applied)
}

func requireRejected(t *testing.T, effect identity.Effect, err error) {
	t.Helper()
	if errors.Is(err, identity.ErrNotImplemented) {
		t.Fatal("unimplemented behavior is not a rejection")
	}
	if err == nil && effect.Kind != identity.Rejected {
		t.Fatalf("expected rejection, got %+v", effect)
	}
}

func TestIdentityTransitionAcceptanceRecoveryDominatesMaximumRecordVersion(t *testing.T) {
	p := loadWirePacket(t)
	h := approvedAnchor(t, p)
	record := parsedVector(t, p, "record").(identity.Record)
	record.Version = identity.MaxCounter
	record = record.Signed(fixturePrivate(t, p, "P0"))
	h = applyEffect(t, h, record, identity.Applied)
	h = applyEffect(t, h, parsedVector(t, p, "succession-1"), identity.Applied)
	if h.Epoch != 2 {
		t.Fatalf("epoch=%d after recovery", h.Epoch)
	}
	if h.Person != identity.KeyID(ed25519.PublicKey(unhex(t, p.Fixtures["P1"].Public))) {
		t.Fatal("recovery did not select P1")
	}
	if h.Version != 0 {
		t.Fatal("old P's version counter survived recovery")
	}
	if len(h.Bindings) != 0 {
		t.Fatal("old-epoch bindings survived recovery")
	}
}

func TestIdentityTransitionAcceptanceHandoverAndRetiredRecoveryKey(t *testing.T) {
	p := loadWirePacket(t)
	h := chainThroughHandover(t, p)
	before := stateBytes(t, h)
	next, effect, err := h.Apply(parsedVector(t, p, "retired-recovery-on-current-predecessor"), time.UnixMilli(1700000010000))
	requireRejected(t, effect, err)
	if !bytes.Equal(before, stateBytes(t, next)) {
		t.Fatal("retired R changed state")
	}
	h = applyEffect(t, h, parsedVector(t, p, "succession-3"), identity.Applied)
	if h.Epoch != 3 {
		t.Fatalf("epoch=%d, want3", h.Epoch)
	}
	if h.Seq != 3 {
		t.Fatalf("recovery sequence=%d, want3", h.Seq)
	}
	if h.Counter != 2 {
		t.Fatalf("recovery counter=%d, want2", h.Counter)
	}
}

func TestIdentityTransitionAcceptanceOldConflictBeforeDuplicateFiltering(t *testing.T) {
	p := loadWirePacket(t)
	h := chainThroughHandover(t, p)
	h = applyEffect(t, h, parsedVector(t, p, "succession-3"), identity.Applied)
	dup := applyEffect(t, h, parsedVector(t, p, "succession-1"), identity.Duplicate)
	if !reflect.DeepEqual(dup, h) {
		t.Fatal("duplicate altered current trust/history")
	}
	frozen := applyEffect(t, h, parsedVector(t, p, "fork-at-anchor"), identity.FrozenEffect)
	if frozen.Trust != identity.Frozen {
		t.Fatal("valid old-predecessor fork did not freeze")
	}
	if len(frozen.Conflicts) == 0 {
		t.Fatal("conflicting signed evidence not retained")
	}
}

func TestIdentityTransitionAcceptanceUnauthenticatedForkCannotFreeze(t *testing.T) {
	p := loadWirePacket(t)
	h := chainThroughHandover(t, p)
	fork := parsedVector(t, p, "fork-at-anchor").(identity.Succession)
	fork.Sig = bytes.Clone(fork.Sig)
	fork.Sig[0] ^= 1
	before := stateBytes(t, h)
	next, effect, err := h.Apply(fork, time.UnixMilli(1700000010000))
	requireRejected(t, effect, err)
	if !bytes.Equal(before, stateBytes(t, next)) {
		t.Fatal("unauthenticated fork mutated or froze identity")
	}
}

func TestIdentityTransitionAcceptanceMissingPredecessorParks(t *testing.T) {
	p := loadWirePacket(t)
	h := approvedAnchor(t, p)
	next, effect, err := h.Apply(parsedVector(t, p, "succession-3"), time.UnixMilli(1700000010000))
	if err != nil {
		t.Fatal(err)
	}
	if effect.Kind != identity.Parked {
		t.Fatalf("ahead proof result=%+v", effect)
	}
	want := identity.DigestOf(parsedVector(t, p, "handover-2").TBS())
	if effect.Need != want {
		t.Fatal("missing proof request names wrong predecessor")
	}
	if next.Person != h.Person {
		t.Fatal("unconnected proof changed person")
	}
	if next.Epoch != h.Epoch {
		t.Fatal("unconnected proof advanced epoch")
	}
	if next.Trust != h.Trust {
		t.Fatal("unconnected proof changed trust")
	}
}

func TestIdentityTransitionAcceptanceFreshSuccessorAndPermanentRevocation(t *testing.T) {
	p := loadWirePacket(t)
	h := approvedAnchor(t, p)
	revocation := parsedVector(t, p, "revocation").(identity.Revocation)
	h = applyEffect(t, h, revocation, identity.Applied)
	h = applyEffect(t, h, parsedVector(t, p, "record"), identity.Applied)
	if !slices.Contains(h.Revoked, revocation.Device) {
		t.Fatal("record omission removed revocation")
	}
	h = applyEffect(t, h, parsedVector(t, p, "succession-1"), identity.Applied)
	h = applyEffect(t, h, parsedVector(t, p, "handover-2"), identity.Applied)
	if !slices.Contains(h.Revoked, revocation.Device) {
		t.Fatal("recovery removed revocation")
	}
	before := stateBytes(t, h)
	next, effect, err := h.Apply(parsedVector(t, p, "reused-person-key"), time.UnixMilli(1700000010000))
	requireRejected(t, effect, err)
	if !bytes.Equal(before, stateBytes(t, next)) {
		t.Fatal("reused old P changed held state")
	}
}

func TestIdentityTransitionAcceptanceSameRecordVersionEquivocation(t *testing.T) {
	p := loadWirePacket(t)
	h := approvedAnchor(t, p)
	r := parsedVector(t, p, "record").(identity.Record)
	h = applyEffect(t, h, r, identity.Applied)
	r.Bindings = nil
	r = r.Signed(fixturePrivate(t, p, "P0"))
	h = applyEffect(t, h, r, identity.FrozenEffect)
	if h.Trust != identity.Frozen {
		t.Fatal("same-version valid record equivocation not frozen")
	}
}

func TestIdentityTransitionAcceptanceBindingMustBeCountersignedByNamedWorkspace(t *testing.T) {
	p := loadWirePacket(t)
	h := approvedAnchor(t, p)
	r := parsedVector(t, p, "record").(identity.Record)
	r.Bindings[0].Acceptance = r.Bindings[0].Acceptance.Signed(fixturePrivate(t, p, "PLegacy"))
	r = r.Signed(fixturePrivate(t, p, "P0"))
	next, effect, err := h.Apply(r, time.UnixMilli(1700000010000))
	requireRejected(t, effect, err)
	if len(next.Bindings) != 0 {
		t.Fatal("invalid binding became authorized")
	}
}
