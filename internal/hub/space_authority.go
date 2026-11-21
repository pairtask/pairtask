package hub

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/pairbook"
)

// Space authority (Pairbook v1): the map each pinned space currently runs
// on. A map becomes active only after the store kept it (its mark already
// durable), and stops being active at its ExpiresAt, by a timer of its own:
// a refresh that fails or is refused never moves that timer. Whoever acts
// on a space's map (the reconciler) is told after every change, outside
// any lock.

// SpaceTiming is the clock and timers space authority runs on; tests
// replace them.
type SpaceTiming struct {
	Now   func() time.Time
	After func(d time.Duration, f func()) (stop func() bool)
}

func realSpaceTiming() SpaceTiming {
	return SpaceTiming{Now: time.Now, After: func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }}
}

// spaceAuthority holds the active maps; safe from any goroutine.
type spaceAuthority struct {
	mu      sync.Mutex
	timing  SpaceTiming
	active  map[string]activeMap
	gen     uint64
	writing map[string]*sync.Mutex // one refresh or unpin per space at a time
	notify  []func(space string)
	closed  bool // Run ended: no timer, no activation, no callback
}

type activeMap struct {
	v    pairbook.VerifiedMap
	gen  uint64
	stop func() bool
}

func newSpaceAuthority() *spaceAuthority {
	return &spaceAuthority{timing: realSpaceTiming(), active: map[string]activeMap{}, writing: map[string]*sync.Mutex{}}
}

// SetSpaceTiming replaces the clock and timers; call it before Run.
func (h *Hub) SetSpaceTiming(t SpaceTiming) {
	h.authority.mu.Lock()
	h.authority.timing = t
	h.authority.mu.Unlock()
}

// OnSpaceChange registers f, called with a space's ID after its active map
// changes or lapses.
func (h *Hub) OnSpaceChange(f func(space string)) {
	h.authority.mu.Lock()
	h.authority.notify = append(h.authority.notify, f)
	h.authority.mu.Unlock()
}

// KeepSpaceMap keeps m as space's map (verified against the pin and the
// stored mark, durable first) and makes it the active one.
// Listeners are told after every lock is released, so one may refresh the
// same space; they read the current state rather than trust the call.
func (h *Hub) KeepSpaceMap(ctx context.Context, space string, m pairbook.SignedSpaceMap) error {
	a := h.authority
	unlock := a.lockSpace(space)
	v, err := h.store.KeepSpaceMap(ctx, space, m, a.now())
	var notify []func(string)
	if err == nil {
		notify = a.activate(space, v)
	}
	unlock()
	tell(notify, space)
	return err
}

// unpinSpace removes space's pin and map and ends its authority, under the
// space's writer lock, so a refresh in flight can't bring it back.
func (h *Hub) unpinSpace(ctx context.Context, space string) error {
	a := h.authority
	unlock := a.lockSpace(space)
	err := h.store.UnpinSpace(ctx, space)
	var notify []func(string)
	if err == nil {
		notify = a.forget(space)
	}
	unlock()
	tell(notify, space)
	return err
}

// SpaceAuthority is space's active map, if it has one that hasn't expired.
func (h *Hub) SpaceAuthority(space string) (pairbook.VerifiedMap, bool) {
	return h.authority.get(space)
}

// restoreSpaceMaps makes each kept map active again, if it still verifies
// against its pin and its own mark now.
func (h *Hub) restoreSpaceMaps(ctx context.Context) {
	spaces, err := h.store.SpaceMapSpaces(ctx)
	if err != nil {
		h.log.Warn("space maps not restored", "err", err)
		return
	}
	for _, space := range spaces {
		h.restoreSpaceMap(ctx, space)
	}
}

func (h *Hub) restoreSpaceMap(ctx context.Context, space string) {
	m, _, found, err := h.store.SpaceMap(ctx, space)
	if err != nil || !found {
		return
	}
	if err := h.KeepSpaceMap(ctx, space, m); err != nil && !errors.Is(err, pairbook.ErrExpired) {
		h.log.Warn("space map not restored", "space", space, "err", err)
	}
}

func (a *spaceAuthority) now() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.timing.Now()
}

// lockSpace serializes refreshes of one space.
func (a *spaceAuthority) lockSpace(space string) func() {
	a.mu.Lock()
	l, ok := a.writing[space]
	if !ok {
		l = &sync.Mutex{}
		a.writing[space] = l
	}
	a.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// activate makes v space's active map, with a timer for its expiry, and
// is who to tell (none for the same map again, or after close).
func (a *spaceAuthority) activate(space string, v pairbook.VerifiedMap) []func(string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	if old, ok := a.active[space]; ok && old.v.Mark == v.Mark {
		return nil
	}
	a.replace(space, v)
	return a.listeners()
}

// replace swaps in v and its timer; a.mu held.
func (a *spaceAuthority) replace(space string, v pairbook.VerifiedMap) {
	if old, ok := a.active[space]; ok {
		old.stop()
	}
	a.gen++
	gen := a.gen
	wait := time.UnixMilli(v.ExpiresAt).Sub(a.timing.Now())
	stop := a.timing.After(wait, func() { a.lapse(space, gen) })
	a.active[space] = activeMap{v: v, gen: gen, stop: stop}
}

// lapse ends space's map gen, unless a newer one replaced it.
func (a *spaceAuthority) lapse(space string, gen uint64) {
	a.mu.Lock()
	cur, ok := a.active[space]
	if a.closed || !ok || cur.gen != gen {
		a.mu.Unlock()
		return
	}
	delete(a.active, space)
	notify := a.listeners()
	a.mu.Unlock()
	tell(notify, space)
}

func (a *spaceAuthority) get(space string) (pairbook.VerifiedMap, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cur, ok := a.active[space]
	if !ok || pairbook.Millis(a.timing.Now()) >= cur.v.ExpiresAt {
		return pairbook.VerifiedMap{}, false
	}
	return cur.v, true
}

// forget drops space's active map (on unpin), and is who to tell.
func (a *spaceAuthority) forget(space string) []func(string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cur, ok := a.active[space]
	if !ok || a.closed {
		return nil
	}
	cur.stop()
	delete(a.active, space)
	return a.listeners()
}

// close stops every timer and every later activation and callback: Run
// has ended.
func (a *spaceAuthority) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	for space, cur := range a.active {
		cur.stop()
		delete(a.active, space)
	}
}

func (a *spaceAuthority) listeners() []func(string) {
	return slices.Clone(a.notify)
}

func tell(fs []func(string), space string) {
	for _, f := range fs {
		f(space)
	}
}

// BindingAcceptance is this workspace's half of binding it to a person:
// the workspace key's signed acceptance of the person's claim, and the
// key. Only the operator socket asks for it.
type BindingAcceptance struct {
	Acceptance   []byte
	WorkspaceKey ed25519.PublicKey
}

// ErrBindingRequest: a malformed lineage or person key ID.
var ErrBindingRequest = errors.New("hub: invalid binding request")

// AcceptBinding signs this workspace's acceptance of belonging to the
// person (key ID person) in lineage, at epoch, at.
func (h *Hub) AcceptBinding(lineage string, epoch uint64, person string, at time.Time) (BindingAcceptance, error) {
	l, ok := lineageFromHex(lineage)
	if !ok || !validKeyID(person) || !signable(epoch, at) {
		return BindingAcceptance{}, ErrBindingRequest
	}
	ep, err := h.PairingEndpoint()
	if err != nil {
		return BindingAcceptance{}, err
	}
	pub := ep.Identity.PublicKey()
	a := identity.Acceptance{Lineage: l, Epoch: epoch, Person: person, Workspace: pairbook.KeyID(pub), At: at}
	sig, err := ep.Identity.Sign(rand.Reader, a.TBS(), crypto.Hash(0))
	if err != nil {
		return BindingAcceptance{}, err
	}
	a.Sig = sig
	return BindingAcceptance{Acceptance: a.Marshal(), WorkspaceKey: pub}, nil
}

// signable: an epoch and time the identity parser will take back.
func signable(epoch uint64, at time.Time) bool {
	if epoch == 0 || epoch > identity.MaxCounter {
		return false
	}
	return at.UnixMilli() >= 0
}

func lineageFromHex(s string) (identity.Lineage, bool) {
	var l identity.Lineage
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(l) || hex.EncodeToString(b) != s {
		return l, false
	}
	copy(l[:], b)
	return l, true
}

// validKeyID: "sha256:" and 64 lowercase hex digits.
func validKeyID(id string) bool {
	h, ok := strings.CutPrefix(id, "sha256:")
	if !ok || len(h) != 64 {
		return false
	}
	b, err := hex.DecodeString(h)
	return err == nil && hex.EncodeToString(b) == h
}
