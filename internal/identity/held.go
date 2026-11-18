package identity

import (
	"crypto/ed25519"
	"time"

	"github.com/pairtask/pairtask/internal/signing"
)

// Trust is what a peer has decided about a lineage.
type Trust string

const (
	// Unapproved: seen, never approved by the operator here.
	Unapproved Trust = "unapproved"
	// Active: approved and not in conflict.
	Active Trust = "active"
	// Frozen: two valid statements claim the same predecessor; the operator
	// must resolve before trust advances.
	Frozen Trust = "frozen"
)

// Proof is one retained recovery statement with where it sits in the
// chain: the signed statement itself, so a conflict at an old predecessor
// can be authenticated later, never a digest alone.
type Proof struct {
	Statement   Statement // a Succession or a Handover
	Digest      Digest
	Predecessor Digest
	Seq         uint64
}

// Conflict is two or more authenticated branches that name the same
// predecessor, kept whole until the operator resolves.
type Conflict struct {
	Predecessor Digest
	Branches    [][]Proof
}

// Held is everything a peer holds about one person (wire-v0 §3), the state
// the transition table of designs/identity/verifier-transitions.json acts
// on. Apply never mutates a Held; it returns the next one.
//
// Keys is an immutable registry: every person and recovery public key the
// lineage has ever presented, by key ID, added only, so any retained proof
// or old-predecessor statement can be verified from Held alone.
type Held struct {
	Lineage  Lineage
	Anchor   Anchor // the genesis statement, verified at approval
	Trust    Trust
	Epoch    uint64
	Counter  uint64
	Seq      uint64 // the recovery sequence applied so far
	Version  uint64 // the record version held for Epoch
	Person   string // P's key ID for Epoch
	Recovery string // R's key ID for Counter; "" with no recovery
	Head     Digest // the last applied recovery statement, or the anchor

	Keys       map[string]ed25519.PublicKey // key ID → public key, for every P and R ever seen
	Persons    []string                     // every P key ID the lineage has used (fresh-key guard)
	Recoveries []string                     // every R key ID the lineage has used

	Record       Record // the record held for (Epoch, Version), whole
	RecordDigest Digest
	Bindings     []Binding
	Revoked      []string // monotonic union of device key IDs

	History   []Proof    // applied recovery statements in order
	Parked    []Proof    // authenticated-in-shape statements awaiting their predecessor
	Conflicts []Conflict // frozen branches, whole
}

// Key is the public key for id, or nil when the lineage never presented it.
func (h Held) Key(id string) ed25519.PublicKey { return h.Keys[id] }

// EffectKind says what Apply did with a statement.
type EffectKind string

const (
	Applied   EffectKind = "applied"
	Duplicate EffectKind = "duplicate"
	// Parked: the predecessor the statement names isn't held yet; Need says which.
	Parked EffectKind = "parked"
	// FrozenEffect: a conflict was found and the lineage is frozen.
	FrozenEffect EffectKind = "frozen"
	Rejected     EffectKind = "rejected"
)

// Effect is Apply's outcome for one statement.
type Effect struct {
	Kind     EffectKind
	Need     Digest    // for Parked
	Branches [][]Proof // for FrozenEffect: each branch from the shared predecessor
	Reason   string    // for Rejected
}

// Apply is one row of the transition table: the next Held, what happened,
// and an error only for malformed or unsigned input. Behaviour follows the
// vectors codex-1 authors; the skeleton declares it.
func (h Held) Apply(st Statement, now time.Time) (Held, Effect, error) {
	return h, Effect{}, ErrNotImplemented
}

// Resolve applies the operator's choice between frozen branches.
func Resolve(h Held, r Resolution) (Held, error) { return h, ErrNotImplemented }

// Clock is the verifier's view of time: the local clock and the persisted
// high-water mark, so an expiry check never moves backwards.
type Clock struct {
	Now  func() time.Time
	Mark time.Time
}

// Effective is max(now, mark).
func (c Clock) Effective() time.Time {
	now := c.Now()
	if c.Mark.After(now) {
		return c.Mark
	}
	return now
}

// Evidence is what a peer keeps about a message's admission, immutable.
type Evidence struct {
	AdmittedAt   time.Time
	State        signing.State
	Epoch        uint64
	Device       string
	RecordDigest Digest
}

// Admit judges a newly received sealed message against the lineage it
// claims (wire-v0 §4): the seal must match the frame it arrived in
// (signing.Seal.Matches) and verify as signing does, then the certificate
// must be valid at the signed time and at the clock's effective time, the
// epoch current and the lineage Active, the device not revoked, and the
// header's workspace bound to the lineage. The Result is the current one;
// the Evidence is stored with the message and never rewritten.
func Admit(seal signing.Seal, frame signing.Frame, h Held, clock Clock) (signing.Result, Evidence) {
	if err := seal.Matches(frame); err != nil {
		return signing.Result{State: signing.Invalid, Reason: err.Error()},
			Evidence{AdmittedAt: clock.Effective(), State: signing.Invalid}
	}
	return signing.Result{State: signing.Unverified, Reason: ErrNotImplemented.Error()},
		Evidence{AdmittedAt: clock.Effective(), State: signing.Unverified}
}
