package hub

import (
	"context"
	"slices"
	"sync"
	"time"
)

// The space reconciler (Pairbook v1) makes this workspace's contacts match
// what the active maps grant: it retires the space-managed contacts that
// no space grants any more, and dials the granted workspaces it isn't paired
// with yet (only those it is the one to dial). It runs after every map
// change or lapse and on a tick, one pass at a time. It never touches a
// manual contact, never dials a workspace the operator refused, and shares
// nothing.

const (
	reconcileEvery = time.Minute
	dialBackoffMin = 5 * time.Second
	dialBackoffMax = 10 * time.Minute
	dialBudget     = 30 * time.Second
)

// spaceReconciler runs the passes. A pass only retires and starts dials:
// each dial runs on a worker of its own (at most maxDials, one per
// target), so a slow dial never delays retiring a revoked contact.
type spaceReconciler struct {
	kick   chan struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup // the loop and every dial worker

	mu      sync.Mutex
	retry   map[string]dialWait // per target: when to dial again
	dialing map[string]bool     // targets with a dial running
}

type dialWait struct {
	next time.Time
	wait time.Duration
}

const maxDials = 4

// startSpaceReconciler runs passes until ctx ends or the hub stops it.
func (h *Hub) startSpaceReconciler(ctx context.Context) {
	if h.spaces == nil || h.store == nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	r := &spaceReconciler{kick: make(chan struct{}, 1), cancel: cancel, retry: map[string]dialWait{}, dialing: map[string]bool{}}
	h.reconciler = r
	h.OnSpaceChange(func(string) { r.poke() })
	r.wg.Add(1)
	go r.loop(ctx, h)
	r.poke()
}

// stopSpaceReconciler cancels the passes and dials and waits for them, so
// none outlives Run.
func (h *Hub) stopSpaceReconciler() {
	if r := h.reconciler; r != nil {
		r.cancel()
		r.wg.Wait()
	}
}

func (r *spaceReconciler) poke() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

func (r *spaceReconciler) loop(ctx context.Context, h *Hub) {
	defer r.wg.Done()
	tick := time.NewTicker(reconcileEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.kick:
		case <-tick.C:
		}
		if err := r.pass(ctx, h); err != nil {
			h.log.Warn("space reconcile", "err", err)
		}
	}
}

// pass is one reconcile: retire now, then start the dials due.
func (r *spaceReconciler) pass(ctx context.Context, h *Hub) error {
	ep, err := h.PairingEndpoint()
	if err != nil {
		return nil // no identity yet: nothing to reconcile
	}
	self := ep.Identity.Fingerprint()
	if err := h.retireUngranted(ctx, self); err != nil {
		return err
	}
	desired, err := h.desiredPeers(ctx, self)
	if err != nil {
		return err
	}
	for _, target := range sortedKeys(desired) {
		if self < target {
			r.maybeDial(ctx, h, spaceDialer{h: h, id: ep.Identity, target: target, peer: desired[target]})
		}
	}
	return nil
}

// retireUngranted ends each space-managed contact no active map grants. The
// grant is checked again for each contact under the contacts lock, which
// installs and map activations also take, so neither can slip in between
// the check and the retire.
func (h *Hub) retireUngranted(ctx context.Context, self string) error {
	managed, err := h.store.ManagedSpaceContacts(ctx)
	if err != nil {
		return err
	}
	for w, epoch := range managed {
		retired, err := h.retireIfUngranted(ctx, self, w, epoch)
		if err != nil {
			return err
		}
		if retired {
			h.PairingChanged()
		}
	}
	return nil
}

func (h *Hub) retireIfUngranted(ctx context.Context, self, w string, epoch int64) (bool, error) {
	h.authority.contactsMu.Lock()
	defer h.authority.contactsMu.Unlock()
	desired, err := h.desiredPeers(ctx, self)
	if err != nil {
		return false, err
	}
	if _, granted := desired[w]; granted {
		return false, nil
	}
	return h.store.RetireSpaceContact(ctx, w, epoch, h.authority.now())
}

// maybeDial starts a worker dialing d's target, unless one runs already,
// too many do, it waits out its backoff, or it is refused or paired.
func (r *spaceReconciler) maybeDial(ctx context.Context, h *Hub, d spaceDialer) {
	if !r.claim(h, d.target) {
		return
	}
	if skip, err := h.skipDial(ctx, d.target); skip || err != nil {
		r.release(h, d.target, nil)
		return
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		dialCtx, cancel := context.WithTimeout(ctx, dialBudget)
		err := d.connect(dialCtx)
		cancel()
		if err != nil {
			h.log.Info("space connect failed", "target", d.target, "err", err)
		}
		r.release(h, d.target, err)
	}()
}

// claim marks target as dialing, if it may be dialed now.
func (r *spaceReconciler) claim(h *Hub, target string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dialing[target] || len(r.dialing) >= maxDials {
		return false
	}
	if w, waiting := r.retry[target]; waiting && h.authority.now().Before(w.next) {
		return false
	}
	r.dialing[target] = true
	return true
}

// release ends target's dial: a failure doubles its wait, within bounds.
func (r *spaceReconciler) release(h *Hub, target string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.dialing, target)
	if err == nil {
		delete(r.retry, target)
		return
	}
	w := r.retry[target]
	w.wait = min(max(2*w.wait, dialBackoffMin), dialBackoffMax)
	w.next = h.authority.now().Add(w.wait)
	r.retry[target] = w
}

// skipDial: the operator refused target, or it is a contact already.
func (h *Hub) skipDial(ctx context.Context, target string) (bool, error) {
	refused, err := h.store.SpaceRefused(ctx, target)
	if err != nil || refused {
		return true, err
	}
	return h.paired(ctx, target)
}

// paired: a contact with key is paired now.
func (h *Hub) paired(ctx context.Context, key string) (bool, error) {
	if _, found, err := h.store.Contact(ctx, key); err != nil || !found {
		return false, err
	}
	return h.store.PeerMessages().Confirmed(ctx, key)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
