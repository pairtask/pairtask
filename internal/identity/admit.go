package identity

import (
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/signing"
)

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
// claims (wire-v0 §4), in this order: the seal must match the frame it
// arrived in and prove what signing checks (signature, chain, claim, not
// future), the certificate must also be valid at the clock's effective
// time, the root must be the current epoch's P of an Active lineage, the
// device must not be revoked, and the header's workspace must be bound to
// the lineage (or covered by a legacy per-workspace certificate until the
// first record is held). The Result is the current one; the Evidence is
// stored with the message and never rewritten.
func Admit(seal signing.Seal, frame signing.Frame, h Held, clock Clock) (signing.Result, Evidence) {
	now := clock.Effective()
	result := admit(seal, frame, h, now)
	return result, Evidence{AdmittedAt: now, State: result.State, Epoch: h.Epoch, Device: seal.KeyID, RecordDigest: h.RecordDigest}
}

func admit(seal signing.Seal, frame signing.Frame, h Held, now time.Time) signing.Result {
	if err := seal.Matches(frame); err != nil {
		return invalid(err.Error())
	}
	result := signing.Verify(&seal, h.roots(seal.Header.Workspace), now)
	if result.State != signing.Verified {
		return result
	}
	if !validAt(seal.Chain[0], now) {
		return invalid("certificate expired at receipt")
	}
	root := KeyID(seal.RootKey)
	switch {
	case root != h.Person:
		return invalid("signed in a retired epoch")
	case slices.Contains(h.Revoked, seal.KeyID):
		return invalid("revoked device")
	case !h.bound(seal.Header.Workspace, seal.Chain[0]):
		return invalid("workspace not bound to this person")
	}
	return result
}

// validAt: the certificate covers the effective receipt time, NotBefore
// inclusive and tolerant of MaxSkew, NotAfter exclusive.
func validAt(c signing.Cert, now time.Time) bool {
	return !now.Add(signing.MaxSkew).Before(c.NotBefore) && now.Before(c.NotAfter)
}

func invalid(reason string) signing.Result {
	return signing.Result{State: signing.Invalid, Reason: reason}
}

// roots presents the lineage to signing.Verify: every P the lineage used
// is known and approved while the lineage is Active, so that a retired
// epoch is judged here as invalid rather than mistaken for an unknown or
// unapproved key; a frozen or unapproved lineage stays unverified.
func (h Held) roots(workspace string) signing.Roots {
	return func(keyID string) (signing.Root, bool) {
		if !slices.Contains(h.Persons, keyID) {
			return signing.Root{}, false
		}
		return signing.Root{Person: true, Workspace: workspace, Approved: h.Trust == Active}, true
	}
}

// bound: the workspace is one of the lineage's current bindings, or the
// legacy exception applies: the operator approved this person's key for
// exactly that workspace under the per-workspace rule (Held.Legacy), the
// certificate is scoped to it, and the lineage is still in its first epoch
// with no record and no recovery applied. A succession ends the exception
// for good; it never reopens.
func (h Held) bound(workspace string, leaf signing.Cert) bool {
	if slices.ContainsFunc(h.Bindings, func(b Binding) bool { return b.Claim.Workspace == workspace }) {
		return true
	}
	return h.legacyOpen() && slices.Contains(h.Legacy, workspace) && leaf.Workspace == workspace
}

// legacyOpen: nothing of the lineage proper has been applied here yet.
func (h Held) legacyOpen() bool {
	return h.Epoch == 1 && h.Version == 0 && len(h.History) == 0
}
