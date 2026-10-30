package hub

import (
	"context"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// startMessageWorker syncs the contacts this side dials, up to
// messageSyncs sessions at once and one per contact: at start, when woken,
// and when a contact's next sync is due. It also collects the message store
// hourly.
func (h *Hub) startMessageWorker(ctx context.Context) {
	h.msgs.init()
	tick := h.pair.cfg.MessageTick
	var ticker *time.Ticker
	if tick == nil {
		ticker = time.NewTicker(MessageActiveInterval)
		tick = ticker.C
	}
	h.pair.workers.Add(1)
	go func() {
		defer h.pair.workers.Done()
		if ticker != nil {
			defer ticker.Stop()
		}
		h.messageLoop(ctx, tick)
	}()
}

// messageSyncs bounds the sessions the worker runs at once, so an
// unreachable contact delays no other and the loop never waits on one.
const messageSyncs = 4

func (h *Hub) messageLoop(ctx context.Context, tick <-chan time.Time) {
	var collected time.Time
	pool := syncPool{slots: make(chan struct{}, messageSyncs)}
	defer pool.wg.Wait()
	h.syncDue(ctx, &pool, true)
	for {
		select {
		case <-ctx.Done():
			return
		case contact := <-h.msgs.wake:
			h.spawnSync(ctx, &pool, contact)
		case <-tick:
			h.syncDue(ctx, &pool, false)
			collected = h.collectMessages(ctx, collected)
		}
	}
}

// syncPool is the worker's running sessions.
type syncPool struct {
	slots chan struct{}
	wg    sync.WaitGroup
}

// spawnSync starts a session with contact when a slot is free; otherwise
// the contact is due at once, and the next tick starts it.
func (h *Hub) spawnSync(ctx context.Context, pool *syncPool, contact string) {
	if !take(pool.slots) {
		h.markDue(contact)
		return
	}
	pool.wg.Add(1)
	go func() {
		defer pool.wg.Done()
		defer func() { <-pool.slots }()
		h.syncContact(ctx, contact)
	}()
}

func take(slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	default:
		return false
	}
}

// markDue makes a contact due on the next tick.
func (h *Hub) markDue(contact string) {
	w := &h.msgs
	w.mu.Lock()
	defer w.mu.Unlock()
	if s := w.syncs[contact]; s != nil {
		s.NextAt = time.Time{}
	}
}

// syncDue starts a session with every contact whose next sync is due, or
// with all of them.
func (h *Hub) syncDue(ctx context.Context, pool *syncPool, all bool) {
	contacts, err := h.store.Contacts(ctx)
	if err != nil {
		h.log.Warn("message sync: contacts unreadable", "err", err)
		return
	}
	now := h.pair.cfg.Clock()
	for _, c := range contacts {
		if all || !now.Before(h.MessageSyncStatus(c.IdentityKey).NextAt) {
			h.spawnSync(ctx, pool, c.IdentityKey)
		}
	}
}

func (h *Hub) collectMessages(ctx context.Context, last time.Time) time.Time {
	now := h.pair.cfg.Clock()
	if now.Sub(last) < messageCollectEvery {
		return last
	}
	if err := h.store.PeerMessages().Collect(ctx, now); err != nil {
		h.log.Warn("message collect failed", "err", err)
		return last
	}
	return now
}

// syncContact runs one session with a confirmed contact this side dials,
// and schedules the next.
func (h *Hub) syncContact(ctx context.Context, contact string) {
	c, ok := h.dialedContact(ctx, contact)
	if !ok {
		return
	}
	end, free := h.beginMessages(contact)
	if !free {
		return // a session with this contact is running already; it isn't a failure
	}
	result, live, err := h.dialSession(ctx, c)
	end(result.Moved())
	if err != nil {
		h.log.Info("message sync failed", "class", classifySync(err))
		h.recordSync(contact, classifySync(err), false)
		h.retryLive(ctx, contact)
		return
	}
	h.recordSync(contact, "", h.messagesWaiting(ctx, contact))
	if live != nil {
		h.goLive(ctx, live)
	}
}

// retryLive redials soon a contact whose link dropped and whose redial
// failed, rather than waiting for the poll's backoff.
func (h *Hub) retryLive(ctx context.Context, contact string) {
	w := &h.live
	w.mu.Lock()
	w.ready()
	linked := w.failures[contact] > 0
	w.mu.Unlock()
	if linked && ctx.Err() == nil {
		time.AfterFunc(h.liveDelay(contact), func() { h.SyncMessages(contact) })
	}
}

// dialedContact is the contact when it is confirmed and this side dials it.
func (h *Hub) dialedContact(ctx context.Context, contact string) (peer.Contact, bool) {
	if !h.canDial() || h.Live(contact) {
		return peer.Contact{}, false
	}
	if !h.messagePins(contact) {
		return peer.Contact{}, false
	}
	c, found, err := h.store.Contact(ctx, contact)
	if err != nil || !found {
		return peer.Contact{}, false
	}
	if !dials(c, h.identity.Fingerprint()) {
		return peer.Contact{}, false
	}
	h.markDialed(contact)
	return c, true
}

// canDial: an identity to present and a way to reach a destination.
func (h *Hub) canDial() bool {
	return h.identity != nil && h.pair.cfg.Dial != nil
}

// dialSession reaches the contact, pins its identity and runs the dialing
// side of a first round. The network is bounded by MessageBudget; the store
// is given PairingGrace beyond it, so a received message settles. When both
// sides speak LiveVersion and have a slot, the connection stays open: the
// caller keeps the returned link.
func (h *Hub) dialSession(ctx context.Context, c peer.Contact) (messaging.Result, *dialedLink, error) {
	netCtx, cancelNet := context.WithTimeout(ctx, MessageBudget)
	defer cancelNet()
	storeCtx, cancelStore := context.WithTimeout(context.WithoutCancel(ctx), MessageBudget+PairingGrace)
	defer cancelStore()
	raw, err := h.pair.cfg.Dial(netCtx, c.Destination)
	if err != nil {
		return messaging.Result{}, nil, errUnreachable{err}
	}
	conn, err := firstcontact.MessageClient(netCtx, raw, h.identity, h.pair.cfg.Clock, c.IdentityKey)
	if err != nil {
		return messaging.Result{}, nil, err
	}
	counted := &messaging.Counted{Frames: conn.MessageFrames(netCtx)}
	s := messaging.Session{Frames: counted, Store: h.store.PeerMessages(), Contact: c.IdentityKey, Now: h.pair.cfg.Clock}
	r, version, err := s.OpenDial(storeCtx)
	if err != nil || version < messaging.LiveVersion {
		conn.Close()
		return r, nil, err
	}
	d, err := h.stayOpen(s, conn, c.IdentityKey)
	if d != nil {
		d.first = FirstRound{Carried: counted, Version: version}
	}
	return r, d, err
}

// stayOpen agrees with the peer whether the connection stays open as a live
// link, and closes it otherwise.
func (h *Hub) stayOpen(s messaging.Session, conn *firstcontact.Conn, contact string) (*dialedLink, error) {
	link, claimed := h.claimLive(contact, false)
	on, err := s.OfferLive(claimed)
	if err == nil && on {
		return &dialedLink{conn: conn, link: link}, nil
	}
	if claimed {
		h.releaseLive(contact, link)
	}
	conn.Close()
	return nil, err
}

// errUnreachable is a dial that never reached the contact.
type errUnreachable struct{ err error }

func (e errUnreachable) Error() string { return "contact unreachable: " + e.err.Error() }
func (e errUnreachable) Unwrap() error { return e.err }

// messagesWaiting: anything still to send or confirm, or recent traffic.
func (h *Hub) messagesWaiting(ctx context.Context, contact string) bool {
	if h.outgoing(ctx, contact) {
		return true
	}
	h.msgs.mu.Lock()
	defer h.msgs.mu.Unlock()
	return h.pair.cfg.Clock().Sub(h.msgs.traffic[contact]) < messageRecent
}

func (h *Hub) markDialed(contact string) {
	w := &h.msgs
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.syncs[contact]
	if s == nil {
		s = &MessageSync{}
		w.syncs[contact] = s
	}
	s.Dials, s.LastAttempt = true, h.pair.cfg.Clock()
}

// recordSync records a session's outcome and when to sync next.
func (h *Hub) recordSync(contact, failure string, active bool) {
	w := &h.msgs
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.syncs[contact]
	if s == nil {
		s = &MessageSync{}
		w.syncs[contact] = s
	}
	now := h.pair.cfg.Clock()
	s.LastFailure = failure
	switch {
	case failure != "":
		s.Failures++
		s.NextAt = now.Add(backoff(s.Failures))
	case active:
		s.Failures, s.LastSuccess, s.NextAt = 0, now, now.Add(MessageActiveInterval)
	default:
		s.Failures, s.LastSuccess, s.NextAt = 0, now, now.Add(MessageIdleInterval)
	}
}
