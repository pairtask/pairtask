package hub

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

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

// spaceKey is a space's ID inside the authority.
type spaceKey string

// spaceAuthority holds the active maps; safe from any goroutine.
type spaceAuthority struct {
	mu      sync.Mutex
	timing  SpaceTiming
	active  map[spaceKey]activeMap
	gen     uint64
	writing map[spaceKey]*sync.Mutex // one refresh or unpin per space at a time
	notify  []func(space string)
	closed  bool // Run ended: no timer, no activation, no callback
}

type activeMap struct {
	v    pairbook.VerifiedMap
	gen  uint64
	stop func() bool
}

func newSpaceAuthority() *spaceAuthority {
	return &spaceAuthority{timing: realSpaceTiming(), active: map[spaceKey]activeMap{}, writing: map[spaceKey]*sync.Mutex{}}
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
	a, k := h.authority, spaceKey(space)
	unlock := a.lockSpace(k)
	v, err := h.store.KeepSpaceMap(ctx, space, m, a.now())
	var notify []func(string)
	if err == nil {
		notify = a.activate(k, v)
	}
	unlock()
	tell(notify, k)
	return err
}

// unpinSpace removes space's pin and map and ends its authority, under the
// space's writer lock, so a refresh in flight can't bring it back.
func (h *Hub) unpinSpace(ctx context.Context, k spaceKey) error {
	a := h.authority
	unlock := a.lockSpace(k)
	err := h.store.UnpinSpace(ctx, string(k))
	var notify []func(string)
	if err == nil {
		notify = a.forget(k)
	}
	unlock()
	tell(notify, k)
	return err
}

// SpaceAuthority is space's active map, if it has one that hasn't expired.
func (h *Hub) SpaceAuthority(space string) (pairbook.VerifiedMap, bool) {
	return h.authority.get(spaceKey(space))
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
		h.restoreSpaceMap(ctx, spaceKey(space))
	}
}

func (h *Hub) restoreSpaceMap(ctx context.Context, k spaceKey) {
	m, _, found, err := h.store.SpaceMap(ctx, string(k))
	if err != nil || !found {
		return
	}
	if err := h.KeepSpaceMap(ctx, string(k), m); err != nil && !errors.Is(err, pairbook.ErrExpired) {
		h.log.Warn("space map not restored", "space", string(k), "err", err)
	}
}

func (a *spaceAuthority) now() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.timing.Now()
}

// lockSpace serializes refreshes of one space.
func (a *spaceAuthority) lockSpace(k spaceKey) func() {
	a.mu.Lock()
	l, ok := a.writing[k]
	if !ok {
		l = &sync.Mutex{}
		a.writing[k] = l
	}
	a.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// activate makes v space's active map, with a timer for its expiry, and
// is who to tell (none for the same map again, or after close).
func (a *spaceAuthority) activate(k spaceKey, v pairbook.VerifiedMap) []func(string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	if old, ok := a.active[k]; ok && old.v.Mark == v.Mark {
		return nil
	}
	a.replace(k, v)
	return a.listeners()
}

// replace swaps in v and its timer; a.mu held.
func (a *spaceAuthority) replace(k spaceKey, v pairbook.VerifiedMap) {
	if old, ok := a.active[k]; ok {
		old.stop()
	}
	a.gen++
	gen := a.gen
	wait := time.UnixMilli(v.ExpiresAt).Sub(a.timing.Now())
	stop := a.timing.After(wait, func() { a.lapse(k, gen) })
	a.active[k] = activeMap{v: v, gen: gen, stop: stop}
}

// lapse ends space's map gen, unless a newer one replaced it.
func (a *spaceAuthority) lapse(k spaceKey, gen uint64) {
	a.mu.Lock()
	if !a.holds(k, gen) {
		a.mu.Unlock()
		return
	}
	delete(a.active, k)
	notify := a.listeners()
	a.mu.Unlock()
	tell(notify, k)
}

// holds: k's active map is still generation gen, and authority is open;
// a.mu held.
func (a *spaceAuthority) holds(k spaceKey, gen uint64) bool {
	cur, ok := a.active[k]
	return ok && !a.closed && cur.gen == gen
}

func (a *spaceAuthority) get(k spaceKey) (pairbook.VerifiedMap, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cur, ok := a.active[k]
	if !ok || pairbook.Millis(a.timing.Now()) >= cur.v.ExpiresAt {
		return pairbook.VerifiedMap{}, false
	}
	return cur.v, true
}

// forget drops space's active map (on unpin), and is who to tell.
func (a *spaceAuthority) forget(k spaceKey) []func(string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cur, ok := a.active[k]
	if !ok || a.closed {
		return nil
	}
	cur.stop()
	delete(a.active, k)
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

func tell(fs []func(string), k spaceKey) {
	for _, f := range fs {
		f(string(k))
	}
}
