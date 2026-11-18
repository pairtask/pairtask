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
	if eff, ok := h.currentEpochOnly(signed{rec.Lineage, rec.Epoch, rec.Person, rec}); !ok {
		return h, eff, nil
	}
	if rec.Recovery != h.Recovery || rec.Counter != h.Counter {
		return h, rejected("record names another recovery state than the one held"), nil
	}
	if err := h.checkBindings(rec.Bindings, rec.Epoch); err != nil {
		return h, rejected(err.Error()), nil
	}
	digest := DigestOf(rec.TBS())
	if h.resolvedAgainst(digest) {
		return h, rejected("resolved against by the operator"), nil
	}
	if rec.Version <= h.Version {
		return h.sameOrOlderRecord(rec, digest)
	}
	return h.withRecord(rec, digest), Effect{Kind: Applied}, nil
}

// sameOrOlderRecord: an older version is ignored, the held one is a
// duplicate, and another record at the held version is equivocation.
func (h Held) sameOrOlderRecord(rec Record, digest Digest) (Held, Effect, error) {
	switch {
	case rec.Version < h.Version:
		return h, rejected("older record version"), nil
	case digest == h.RecordDigest:
		return h, Effect{Kind: Duplicate}, nil
	}
	held := Proof{Statement: h.Record, Digest: h.RecordDigest, lineage: h.Lineage}
	return h.freeze(h.RecordDigest, [][]Proof{{held}, {{Statement: rec, Digest: digest, lineage: h.Lineage}}})
}

func (h Held) withRecord(rec Record, digest Digest) Held {
	h.Record, h.RecordDigest, h.Version, h.Bindings = rec, digest, rec.Version, rec.Bindings
	h.Revoked = union(h.Revoked, rec.Revoked)
	return h
}

// signed is what a P-signed statement of one epoch says about itself.
type signed struct {
	lineage Lineage
	epoch   uint64
	person  string
	st      Statement
}

// currentEpochOnly checks what every P-signed statement of the current
// epoch needs: the lineage, an Active trust, the current epoch and P, and
// P's signature. It says what to do otherwise.
func (h Held) currentEpochOnly(s signed) (Effect, bool) {
	switch {
	case s.lineage != h.Lineage:
		return rejected("another lineage"), false
	case h.Trust == Frozen:
		return rejected("lineage is frozen"), false
	case s.epoch < h.Epoch:
		return rejected("retired epoch"), false
	case s.epoch > h.Epoch:
		return Effect{Kind: Parked, Need: h.Head, Reason: "epoch ahead of the held recovery chain"}, false
	case !h.signedByPerson(s):
		return rejected("not signed by the current person key"), false
	}
	return Effect{}, true
}

// signedByPerson: the statement names the current P and carries its signature.
func (h Held) signedByPerson(s signed) bool {
	return s.person == h.Person && check(h.Keys[s.person], s.st.TBS(), s.st.Signature()) == nil
}

// checkBindings: every binding is consistent, for this epoch and lineage,
// claimed by P and accepted by a workspace key this peer knows under that ID.
func (h Held) checkBindings(bindings []Binding, epoch uint64) error {
	for _, b := range bindings {
		if err := b.Consistent(); err != nil {
			return err
		}
		if !h.bindingAbout(b, epoch) {
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

// bindingAbout: the binding is about this lineage, epoch and person.
func (h Held) bindingAbout(b Binding, epoch uint64) bool {
	return b.Claim.Lineage == h.Lineage && b.Claim.Epoch == epoch && b.Acceptance.Person == h.Person
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
	if eff, ok := h.currentEpochOnly(signed{v.Lineage, v.Epoch, h.Person, v}); !ok {
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
	if eff, ok := h.currentEpochOnly(signed{n.Lineage, n.Epoch, h.Person, n}); !ok {
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
	if h.Trust == Frozen && p.lineage == h.Lineage {
		return h.grow(p)
	}
	if reason := h.refuses(p); reason != "" {
		return h, rejected(reason), nil
	}
	if p.Predecessor == h.Head {
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

// grow, while frozen, extends the conflict branch whose head the statement
// names, so the displayed set changes and a stale confirmation is refused;
// anything else is kept out until the operator resolves.
func (h Held) grow(p Proof) (Held, Effect, error) {
	if len(h.Conflicts) == 0 {
		return h, rejected("lineage is frozen"), nil
	}
	c := h.Conflicts[len(h.Conflicts)-1]
	i := slices.IndexFunc(c.Branches, func(b []Proof) bool { return head(b) == p.Predecessor })
	if i < 0 && p.Predecessor != c.Predecessor {
		return h, rejected("lineage is frozen"), nil
	}
	if slices.ContainsFunc(c.Branches, func(b []Proof) bool { return contains(b, p.Digest) }) {
		return h, Effect{Kind: Duplicate}, nil
	}
	end, err := h.endOf(c, i)
	if err != nil {
		return h, rejected(err.Error()), nil
	}
	if _, err := end.authenticate(p); err != nil {
		return h, rejected(err.Error()), nil
	}
	c.Branches = grown(c.Branches, i, p)
	h.Conflicts = append(slices.Clone(h.Conflicts[:len(h.Conflicts)-1]), c)
	return h, Effect{Kind: FrozenEffect, Branches: c.Branches}, nil
}

// grown is the branch set with p added: extending branch i, or opening a
// new branch at the shared predecessor when i is -1.
func grown(branches [][]Proof, i int, p Proof) [][]Proof {
	out := slices.Clone(branches)
	if i < 0 {
		return append(out, []Proof{p})
	}
	out[i] = append(slices.Clone(out[i]), p)
	return out
}

// endOf is the state at the end of conflict branch i, or at the shared
// predecessor when i is -1.
func (h Held) endOf(c Conflict, i int) (Held, error) {
	if i < 0 {
		return h.replay(c.Predecessor, nil)
	}
	branch := c.Branches[i]
	if head(branch) == h.Head {
		return h, nil
	}
	if isRecord(branch[len(branch)-1]) {
		return h, ErrMalformed
	}
	return h.replay(c.Predecessor, branch)
}

// replay is the state at predecessor with branch applied after it.
func (h Held) replay(predecessor Digest, branch []Proof) (Held, error) {
	next, _, ok := h.stateAt(predecessor)
	if !ok {
		return h, ErrMalformed
	}
	for _, q := range branch {
		after, err := next.authenticate(q)
		if err != nil {
			return h, err
		}
		next = after.record(q)
	}
	return next, nil
}

// refuses is why a recovery statement isn't considered at all, or "".
func (h Held) refuses(p Proof) string {
	switch {
	case p.lineage != h.Lineage:
		return "another lineage"
	case h.Trust == Frozen:
		return "lineage is frozen"
	case h.resolvedAgainst(p.Digest) || h.resolvedAgainst(p.Predecessor):
		return "resolved against by the operator"
	}
	return ""
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
	if !h.activeRecovery(s.Recovery, s.Counter, s.Epoch) || s.Person != h.Person {
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
	if !h.activeRecovery(s.Recovery, s.Counter, s.Epoch) {
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

// activeRecovery: the statement names exactly the recovery key, counter
// and epoch active here, not merely ones the lineage once had.
func (h Held) activeRecovery(recovery string, counter, epoch uint64) bool {
	return recovery == h.Recovery && counter == h.Counter && epoch == h.Epoch
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

// unpark applies every parked statement whose predecessor is now known: the
// first on the head advances, a second one for the same predecessor is a
// conflict, so nothing valid is stranded in the queue.
func (h Held) unpark() (Held, Effect, error) {
	effect := Effect{Kind: Applied}
	for {
		i := slices.IndexFunc(h.Parked, func(q Proof) bool { return h.knows(q.Predecessor) })
		if i < 0 {
			return h, effect, nil
		}
		p := h.Parked[i]
		h.Parked = slices.Delete(slices.Clone(h.Parked), i, i+1)
		next, eff, err := h.applyRecovery(p)
		if err != nil {
			return next, eff, err
		}
		h = next
		if eff.Kind == FrozenEffect {
			effect = eff
		}
	}
}

// knows: d is the head, the anchor or an applied proof.
func (h Held) knows(d Digest) bool {
	return d == h.Head || d == DigestOf(h.Anchor.TBS()) || slices.ContainsFunc(h.History, func(p Proof) bool { return p.Digest == d })
}

// freeze records a conflict and stops trust from advancing.
func (h Held) freeze(predecessor Digest, branches [][]Proof) (Held, Effect, error) {
	h.Trust = Frozen
	h.Conflicts = append(slices.Clone(h.Conflicts), Conflict{Predecessor: predecessor, Branches: branches})
	return h, Effect{Kind: FrozenEffect, Branches: branches}, nil
}

// resolvedAgainst: d is a statement on a branch the operator rejected.
func (h Held) resolvedAgainst(d Digest) bool {
	return slices.ContainsFunc(h.Conflicts, func(c Conflict) bool { return c.rejects(d) })
}

// rejects: the conflict was resolved and d is on a branch not chosen.
func (c Conflict) rejects(d Digest) bool {
	if c.Chosen == (Digest{}) {
		return false
	}
	return slices.ContainsFunc(c.Branches, func(b []Proof) bool { return head(b) != c.Chosen && contains(b, d) })
}

func contains(b []Proof, d Digest) bool {
	return slices.ContainsFunc(b, func(p Proof) bool { return p.Digest == d })
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
	c, i, err := h.choice(r)
	if err != nil {
		return h, err
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

// choice is the frozen conflict and the branch r chooses, when r names
// exactly what was displayed: the lineage, the branch set, the chosen head
// and every rejected head.
func (h Held) choice(r Resolution) (Conflict, int, error) {
	if !h.resolvable(r.Lineage) {
		return Conflict{}, 0, ErrMalformed
	}
	c := h.Conflicts[len(h.Conflicts)-1]
	i := slices.IndexFunc(c.Branches, func(b []Proof) bool { return head(b) == r.Chosen })
	if i < 0 {
		return Conflict{}, 0, ErrMalformed
	}
	if !c.displayed(r, i) {
		return Conflict{}, 0, ErrMalformed
	}
	return c, i, nil
}

// resolvable: this lineage is frozen on a recorded conflict.
func (h Held) resolvable(l Lineage) bool {
	frozen := h.Trust == Frozen && len(h.Conflicts) > 0
	return frozen && l == h.Lineage
}

// displayed: r's branch set and rejected heads are this conflict's, with
// branch i chosen.
func (c Conflict) displayed(r Resolution, i int) bool {
	return r.BranchSet == BranchSetDigest(c.Branches) && slices.Equal(r.Rejected, rejectedHeads(c.Branches, i))
}

// onto rebuilds the state on the chosen branch of c: for a record
// equivocation, the current chain with the chosen record; for the branch
// already applied, the state as it is; for another branch, from the
// predecessor through that branch's proofs. Whatever branch wins, what is
// monotonic carries over: revoked devices, and every key the lineage used.
func (h Held) onto(c Conflict, branch []Proof) (Held, error) {
	if rec, ok := branch[len(branch)-1].Statement.(Record); ok {
		return h.withRecord(rec, head(branch)), nil
	}
	if head(branch) == h.Head {
		return h, nil
	}
	next, err := h.replay(c.Predecessor, branch)
	if err != nil {
		return h, err
	}
	return h.carry(next), nil
}

// carry moves the monotonic and local parts of h onto a rebuilt state.
func (h Held) carry(next Held) Held {
	next.Revoked = union(h.Revoked, next.Revoked)
	next.Persons = union(h.Persons, next.Persons)
	next.Recoveries = union(h.Recoveries, next.Recoveries)
	for id, pub := range h.Keys {
		next.Keys = withKey(next.Keys, id, pub)
	}
	next.Parked, next.Resolutions, next.Conflicts, next.Workspaces = h.Parked, h.Resolutions, h.Conflicts, h.Workspaces
	return next
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

func isRecord(p Proof) bool {
	_, ok := p.Statement.(Record)
	return ok
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
