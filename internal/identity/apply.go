package identity

import (
	"crypto/ed25519"
	"crypto/sha256"
	"slices"
	"time"
)

// Apply is one row of the transition table (designs/identity/
// verifier-transitions.json, wire-v0 §5 and §7): the next Held, what
// happened, and an error only for input that isn't a well-formed signed
// statement of a kind Apply takes. Apply never trusts arrival order: a
// statement is authenticated against the state at the predecessor it names,
// and a different valid statement for a predecessor already taken freezes
// the lineage.
func (h Held) Apply(st Statement, now time.Time) (Held, Effect, error) {
	switch s := st.(type) {
	case Anchor:
		return h.applyAnchor(s)
	case Record:
		return h.applyRecord(s)
	case Revocation:
		return h.applyRevocation(s)
	case Succession:
		return h.applyRecovery(proofOf(s, s.Lineage, s.Prev, s.Seq))
	case Handover:
		return h.applyRecovery(proofOf(s, s.Lineage, s.Prev, s.Seq))
	case Notice:
		return h.applyNotice(s)
	}
	return h, rejected("not a statement Apply takes"), nil
}

func proofOf(st Statement, l Lineage, prev Digest, seq uint64) Proof {
	return Proof{Statement: st, Digest: DigestOf(st.TBS()), Predecessor: prev, Seq: seq, lineage: l}
}

func rejected(reason string) Effect { return Effect{Kind: Rejected, Reason: reason} }

// applyAnchor makes an unapproved candidate from a lineage's genesis.
func (h Held) applyAnchor(a Anchor) (Held, Effect, error) {
	if err := a.Check(); err != nil {
		return h, Effect{}, err
	}
	digest := DigestOf(a.TBS())
	if h.Trust != "" {
		if DigestOf(h.Anchor.TBS()) == digest {
			return h, Effect{Kind: Duplicate}, nil
		}
		return h, rejected("another anchor for a lineage already held"), nil
	}
	return genesis(a, h.Workspaces), Effect{Kind: Applied}, nil
}

// genesis is the state right after a verified anchor.
func genesis(a Anchor, workspaces map[string]ed25519.PublicKey) Held {
	next := Held{Lineage: a.Lineage(), Anchor: a, Trust: Unapproved, Epoch: 1, Head: DigestOf(a.TBS()),
		Keys: map[string]ed25519.PublicKey{}, Workspaces: workspaces}
	next.Person = KeyID(a.Person)
	next.Keys[next.Person] = a.Person
	next.Persons = []string{next.Person}
	if a.Recovery != nil {
		next.Counter, next.Recovery = 1, KeyID(a.Recovery)
		next.Keys[next.Recovery] = a.Recovery
		next.Recoveries = []string{next.Recovery}
	}
	return next
}

// Approve is the operator's local approval of a candidate: P, R and L
// together, as displayed.
func (h Held) Approve() (Held, error) {
	if h.Trust != Unapproved {
		return h, ErrMalformed
	}
	h.Trust = Active
	return h, nil
}

// applyRecord replaces bindings and merges revocations for a higher
// version of the current epoch; an equal version with another digest is
// equivocation and freezes.
func (h Held) applyRecord(rec Record) (Held, Effect, error) {
	if eff, ok := h.currentEpochOnly(rec.Lineage, rec.Epoch, rec.Person, rec.Sig, rec.TBS()); !ok {
		return h, eff, nil
	}
	if rec.Recovery != h.Recovery || rec.Counter != h.Counter {
		return h, rejected("record names another recovery state than the one held"), nil
	}
	digest := DigestOf(rec.TBS())
	switch {
	case rec.Version < h.Version:
		return h, rejected("older record version"), nil
	case rec.Version == h.Version && digest == h.RecordDigest:
		return h, Effect{Kind: Duplicate}, nil
	case rec.Version == h.Version:
		held := Proof{Statement: h.Record, Digest: h.RecordDigest, lineage: h.Lineage}
		return h.freeze(h.RecordDigest, [][]Proof{{held}, {{Statement: rec, Digest: digest, lineage: h.Lineage}}})
	}
	if err := h.checkBindings(rec.Bindings, rec.Epoch); err != nil {
		return h, rejected(err.Error()), nil
	}
	return h.withRecord(rec, digest), Effect{Kind: Applied}, nil
}

func (h Held) withRecord(rec Record, digest Digest) Held {
	h.Record, h.RecordDigest, h.Version, h.Bindings = rec, digest, rec.Version, rec.Bindings
	h.Revoked = union(h.Revoked, rec.Revoked)
	return h
}

// currentEpochOnly checks what every P-signed statement of the current
// epoch needs: the lineage, an Active trust, the current epoch and P, and
// P's signature. It says what to do otherwise.
func (h Held) currentEpochOnly(l Lineage, epoch uint64, person string, sig, tbs []byte) (Effect, bool) {
	switch {
	case l != h.Lineage:
		return rejected("another lineage"), false
	case h.Trust == Frozen:
		return rejected("lineage is frozen"), false
	case epoch < h.Epoch:
		return rejected("retired epoch"), false
	case epoch > h.Epoch:
		return Effect{Kind: Parked, Need: h.Head, Reason: "epoch ahead of the held recovery chain"}, false
	case person != h.Person || check(h.Keys[person], tbs, sig) != nil:
		return rejected("not signed by the current person key"), false
	}
	return Effect{}, true
}

// checkBindings: every binding is consistent, for this epoch and lineage,
// claimed by P and accepted by a workspace key this peer knows under that ID.
func (h Held) checkBindings(bindings []Binding, epoch uint64) error {
	for _, b := range bindings {
		if err := b.Consistent(); err != nil {
			return err
		}
		if b.Claim.Lineage != h.Lineage || b.Claim.Epoch != epoch || b.Acceptance.Person != h.Person {
			return ErrMalformed
		}
		if err := b.Claim.Check(h.Keys[h.Person]); err != nil {
			return err
		}
		if err := h.checkAcceptance(b.Acceptance); err != nil {
			return err
		}
	}
	return nil
}

// checkAcceptance verifies W's signature under a registered key whose ID
// is the one the acceptance names; an unknown or mismatched key never
// authorizes a binding.
func (h Held) checkAcceptance(a Acceptance) error {
	pub, ok := h.Workspaces[a.Workspace]
	if !ok || KeyID(pub) != a.Workspace {
		return ErrBadSignature
	}
	return a.Check(pub)
}

// applyRevocation adds a device to the monotonic revoked set.
func (h Held) applyRevocation(v Revocation) (Held, Effect, error) {
	if eff, ok := h.currentEpochOnly(v.Lineage, v.Epoch, h.Person, v.Sig, v.TBS()); !ok {
		return h, eff, nil
	}
	if slices.Contains(h.Revoked, v.Device) {
		return h, Effect{Kind: Duplicate}, nil
	}
	h.Revoked = union(h.Revoked, []string{v.Device})
	return h, Effect{Kind: Applied}, nil
}

// applyNotice checks a new-device notice; it changes no held state, since
// the device list is the person's own.
func (h Held) applyNotice(n Notice) (Held, Effect, error) {
	if eff, ok := h.currentEpochOnly(n.Lineage, n.Epoch, h.Person, n.Sig, n.TBS()); !ok {
		return h, eff, nil
	}
	return h, Effect{Kind: Applied}, nil
}

// applyRecovery places a succession or handover in the recovery chain: on
// the head it advances the lineage; on a predecessor already taken it is a
// duplicate or a conflict; on an unknown predecessor it is parked. A
// statement the operator already resolved against, or one descending from
// it, is rejected without freezing again.
func (h Held) applyRecovery(p Proof) (Held, Effect, error) {
	switch {
	case p.lineage != h.Lineage:
		return h, rejected("another lineage"), nil
	case h.Trust == Frozen:
		return h, rejected("lineage is frozen"), nil
	case h.resolvedAgainst(p.Digest) || h.resolvedAgainst(p.Predecessor):
		return h, rejected("resolved against by the operator"), nil
	case p.Predecessor == h.Head:
		return h.advance(p)
	}
	at, successor, ok := h.stateAt(p.Predecessor)
	if !ok {
		return h.park(p)
	}
	if successor.Digest == p.Digest {
		return h, Effect{Kind: Duplicate}, nil
	}
	if _, err := at.authenticate(p); err != nil {
		return h, rejected(err.Error()), nil
	}
	return h.freeze(p.Predecessor, [][]Proof{h.branchFrom(p.Predecessor), {p}})
}

// advance applies an authenticated recovery statement on the head.
func (h Held) advance(p Proof) (Held, Effect, error) {
	next, err := h.authenticate(p)
	if err != nil {
		return h, rejected(err.Error()), nil
	}
	return next.record(p).unpark()
}

// record appends an authenticated proof to the chain.
func (h Held) record(p Proof) Held {
	h.Head, h.Seq = p.Digest, p.Seq
	h.History = append(slices.Clone(h.History), p)
	return h
}

// authenticate checks a recovery statement against this state as its
// predecessor and returns the state after it, without recording it.
func (h Held) authenticate(p Proof) (Held, error) {
	if p.Seq != h.Seq+1 || h.Recovery == "" {
		return h, ErrMalformed
	}
	switch s := p.Statement.(type) {
	case Succession:
		return h.succeed(s)
	case Handover:
		return h.handOver(s)
	}
	return h, ErrMalformed
}

func (h Held) succeed(s Succession) (Held, error) {
	if s.Epoch != h.Epoch || s.Person != h.Person || s.Recovery != h.Recovery || s.Counter != h.Counter {
		return h, ErrMalformed
	}
	if err := s.Check(h.Keys[h.Recovery]); err != nil {
		return h, err
	}
	id := KeyID(s.NextPerson)
	if !h.fresh(id) {
		return h, ErrFreshKey
	}
	h.Epoch, h.Person, h.Version = h.Epoch+1, id, 0
	h.Record, h.RecordDigest, h.Bindings = Record{}, Digest{}, nil
	h.Keys = withKey(h.Keys, id, s.NextPerson)
	h.Persons = append(slices.Clone(h.Persons), id)
	return h, nil
}

func (h Held) handOver(s Handover) (Held, error) {
	if s.Epoch != h.Epoch || s.Recovery != h.Recovery || s.Counter != h.Counter {
		return h, ErrMalformed
	}
	if err := s.Check(h.Keys[h.Recovery]); err != nil {
		return h, err
	}
	id := KeyID(s.NextRecovery)
	if !h.fresh(id) {
		return h, ErrFreshKey
	}
	h.Counter, h.Recovery = h.Counter+1, id
	h.Keys = withKey(h.Keys, id, s.NextRecovery)
	h.Recoveries = append(slices.Clone(h.Recoveries), id)
	return h, nil
}

// fresh: the lineage has never used id as a person or recovery key.
func (h Held) fresh(id string) bool {
	return !slices.Contains(h.Persons, id) && !slices.Contains(h.Recoveries, id)
}

// stateAt replays the retained chain to the state whose head is
// predecessor, with the proof that followed it there (zero at the head).
func (h Held) stateAt(predecessor Digest) (Held, Proof, bool) {
	at := genesis(h.Anchor, h.Workspaces)
	at.Trust = h.Trust
	for i := 0; i <= len(h.History); i++ {
		if at.Head == predecessor {
			if i < len(h.History) {
				return at, h.History[i], true
			}
			return at, Proof{}, true
		}
		if i == len(h.History) {
			break
		}
		next, err := at.authenticate(h.History[i])
		if err != nil {
			return h, Proof{}, false
		}
		at = next.record(h.History[i])
	}
	return h, Proof{}, false
}

// branchFrom is the applied chain after predecessor.
func (h Held) branchFrom(predecessor Digest) []Proof {
	for i, p := range h.History {
		if p.Predecessor == predecessor {
			return slices.Clone(h.History[i:])
		}
	}
	return nil
}

// park keeps a statement whose predecessor isn't held, bounded.
func (h Held) park(p Proof) (Held, Effect, error) {
	if slices.ContainsFunc(h.Parked, func(q Proof) bool { return q.Digest == p.Digest }) {
		return h, Effect{Kind: Duplicate}, nil
	}
	parked := append(slices.Clone(h.Parked), p)
	if len(parked) > MaxParked {
		parked = parked[len(parked)-MaxParked:]
	}
	h.Parked = parked
	return h, Effect{Kind: Parked, Need: p.Predecessor}, nil
}

// MaxParked bounds the statements kept while their predecessors are fetched.
const MaxParked = 32

// unpark applies any parked statement whose predecessor is now the head.
func (h Held) unpark() (Held, Effect, error) {
	for i, p := range h.Parked {
		if p.Predecessor == h.Head {
			h.Parked = slices.Delete(slices.Clone(h.Parked), i, i+1)
			next, _, err := h.applyRecovery(p)
			return next, Effect{Kind: Applied}, err
		}
	}
	return h, Effect{Kind: Applied}, nil
}

// freeze records a conflict and stops trust from advancing.
func (h Held) freeze(predecessor Digest, branches [][]Proof) (Held, Effect, error) {
	h.Trust = Frozen
	h.Conflicts = append(slices.Clone(h.Conflicts), Conflict{Predecessor: predecessor, Branches: branches})
	return h, Effect{Kind: FrozenEffect, Branches: branches}, nil
}

// resolvedAgainst: d is a statement on a branch the operator rejected.
func (h Held) resolvedAgainst(d Digest) bool {
	for _, c := range h.Conflicts {
		if c.Chosen == (Digest{}) {
			continue
		}
		for _, b := range c.Branches {
			if head(b) != c.Chosen && slices.ContainsFunc(b, func(p Proof) bool { return p.Digest == d }) {
				return true
			}
		}
	}
	return false
}

// Resolve applies the operator's choice between frozen branches: the state
// is rebuilt onto the chosen branch, the other branches stay retained as
// rejected, and trust returns to Active. The resolution must be signed by
// local, this workspace's own identity key, which the caller supplies: a
// resolution signed by any other key, a remote workspace's included, is
// refused. A choice outside the displayed set is refused too.
func Resolve(h Held, r Resolution, local ed25519.PublicKey) (Held, error) {
	if err := r.Check(local); err != nil {
		return h, err
	}
	if h.Trust != Frozen || len(h.Conflicts) == 0 || r.Lineage != h.Lineage {
		return h, ErrMalformed
	}
	c := h.Conflicts[len(h.Conflicts)-1]
	i := slices.IndexFunc(c.Branches, func(b []Proof) bool { return head(b) == r.Chosen })
	if i < 0 || r.BranchSet != BranchSetDigest(c.Branches) || !slices.Equal(r.Rejected, rejectedHeads(c.Branches, i)) {
		return h, ErrMalformed
	}
	next, err := h.onto(c, c.Branches[i])
	if err != nil {
		return h, err
	}
	c.Chosen = r.Chosen
	next.Conflicts = append(slices.Clone(h.Conflicts[:len(h.Conflicts)-1]), c)
	next.Resolutions = append(slices.Clone(h.Resolutions), r)
	next.Trust = Active
	return next, nil
}

// onto rebuilds the state on the chosen branch of c: for a recovery
// conflict, from the predecessor through the branch's proofs; for a record
// equivocation, the current chain with the chosen record.
func (h Held) onto(c Conflict, branch []Proof) (Held, error) {
	if rec, ok := branch[len(branch)-1].Statement.(Record); ok {
		return h.withRecord(rec, head(branch)), nil
	}
	next, _, ok := h.stateAt(c.Predecessor)
	if !ok {
		return h, ErrMalformed
	}
	for _, p := range branch {
		after, err := next.authenticate(p)
		if err != nil {
			return h, err
		}
		next = after.record(p)
	}
	next.Parked, next.Resolutions, next.Conflicts = h.Parked, h.Resolutions, h.Conflicts
	return next, nil
}

// rejectedHeads are the heads of every branch but the chosen one, sorted.
func rejectedHeads(branches [][]Proof, chosen int) []Digest {
	var out []Digest
	for i, b := range branches {
		if i != chosen {
			out = append(out, head(b))
		}
	}
	slices.SortFunc(out, func(a, b Digest) int { return slices.Compare(a[:], b[:]) })
	return out
}

// BranchSetDigest is the digest the operator's confirmation is bound to:
// SHA-256 over the sorted raw digests of every statement displayed.
func BranchSetDigest(branches [][]Proof) Digest {
	var all []Digest
	for _, b := range branches {
		for _, p := range b {
			all = append(all, p.Digest)
		}
	}
	slices.SortFunc(all, func(a, b Digest) int { return slices.Compare(a[:], b[:]) })
	sum := sha256.New()
	for _, d := range all {
		sum.Write(d[:])
	}
	return Digest(sum.Sum(nil))
}

func head(b []Proof) Digest {
	if len(b) == 0 {
		return Digest{}
	}
	return b[len(b)-1].Digest
}

// union merges sorted, unique ID lists.
func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, id := range b {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

func withKey(keys map[string]ed25519.PublicKey, id string, pub ed25519.PublicKey) map[string]ed25519.PublicKey {
	out := make(map[string]ed25519.PublicKey, len(keys)+1)
	for k, v := range keys {
		out[k] = v
	}
	out[id] = pub
	return out
}
