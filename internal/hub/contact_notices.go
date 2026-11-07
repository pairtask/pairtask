package hub

import (
	"context"
	"errors"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

// Contact notices (#276, D3/D8/D9). A local pause, resume or unpair queues
// one notice; the message worker attempts it right after the change, at
// start and on each tick while it is due, in a control session it dials
// whenever this side holds the peer's address, whichever side normally
// dials. A contact has at most one session at a time, message or control,
// and at most one control session, either role, per messageBackoffMin; the
// notice's own schedule is the durable one.

// errControlTooSoon refuses a control session within the contact's bound.
var errControlTooSoon = errors.New("a control session with this contact ran within the last minute")

// noticesDue asks the worker for a notice pass at once. Safe from any
// goroutine; it never blocks.
func (h *Hub) noticesDue() {
	h.msgs.init()
	select {
	case h.msgs.notices <- struct{}{}:
	default: // a pass is asked for already
	}
}

// noticePass starts an attempt at every notice due now; one without a free
// slot stays due for the next pass.
func (h *Hub) noticePass(ctx context.Context, pool *syncPool) {
	due, err := h.store.PeerMessages().PendingNotices(ctx, h.pair.cfg.Clock())
	if err != nil {
		h.log.Warn("contact notices unreadable", "err", err)
		return
	}
	for _, n := range due {
		pool.spawn(func() { h.deliverNotice(ctx, n) })
	}
}

// deliverNotice attempts n when this side holds the peer's address and no
// other session with the contact runs.
func (h *Hub) deliverNotice(ctx context.Context, n store.NoticeDue) {
	c, ok := h.noticeContact(ctx, n.Contact)
	if !ok {
		return // it stays pending: the peer learns when it next connects
	}
	end, free := h.beginMessages(n.Contact)
	if !free {
		return
	}
	defer end(false)
	h.controlDial(ctx, c, n.Gen)
}

// noticeContact is the contact when this side can dial it.
func (h *Hub) noticeContact(ctx context.Context, contact string) (peer.Contact, bool) {
	if !h.canDial() {
		return peer.Contact{}, false
	}
	c, found, err := h.store.Contact(ctx, contact)
	return c, err == nil && found && c.Destination != ""
}

// controlDial runs one dialled control session with c, within the
// contact's bound, and records the attempt: on the notice that went out,
// else on scheduled, the notice it was for (0: none). The caller holds the
// contact's claim.
func (h *Hub) controlDial(ctx context.Context, c peer.Contact, scheduled int64) {
	if !h.takeControl(c.IdentityKey) {
		return
	}
	run, err := h.dialControl(ctx, c)
	if err != nil {
		h.log.Info("control session failed", "class", classifySync(err))
	}
	h.controlEnded(ctx, run, scheduled)
}

// takeControl admits a control session with contact unless one was
// admitted within messageBackoffMin, and marks it.
func (h *Hub) takeControl(contact string) bool {
	w := &h.msgs
	w.init()
	w.mu.Lock()
	defer w.mu.Unlock()
	now := h.pair.cfg.Clock()
	if now.Before(w.lastControl[contact].Add(messageBackoffMin)) {
		return false
	}
	w.lastControl[contact] = now
	return true
}

// dialControl dials c on ControlALPN and runs the dialing side. The network
// is bounded by MessageBudget; the store is given PairingGrace beyond it.
func (h *Hub) dialControl(ctx context.Context, c peer.Contact) (controlRun, error) {
	netCtx, cancelNet := context.WithTimeout(ctx, MessageBudget)
	defer cancelNet()
	storeCtx, cancelStore := context.WithTimeout(context.WithoutCancel(ctx), MessageBudget+PairingGrace)
	defer cancelStore()
	failed := controlRun{contact: c.IdentityKey}
	admitted, err := h.store.PeerMessages().AdmitControl(storeCtx, c.IdentityKey)
	if err != nil {
		return failed, err
	}
	conn, err := h.openControl(netCtx, c)
	if err != nil {
		return failed, err
	}
	defer conn.Close()
	s := &noticeStore{ControlStore: admitted, contact: c.IdentityKey}
	r, err := messaging.ControlSession{Frames: conn.MessageFrames(netCtx), Store: s, Contact: c.IdentityKey}.Dial(storeCtx)
	return s.run(r), err
}

// openControl reaches c and pins its identity on ControlALPN.
func (h *Hub) openControl(ctx context.Context, c peer.Contact) (*firstcontact.Conn, error) {
	raw, err := h.pair.cfg.Dial(ctx, c.Destination)
	if err != nil {
		return nil, errUnreachable{err}
	}
	return firstcontact.ControlClient(ctx, raw, h.identity, h.pair.cfg.Clock, c.IdentityKey)
}

// controlPins admits a control session from any contact row this side
// retains: active, paused, unpaired or unpaired by the other side.
func (h *Hub) controlPins(fingerprint string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), PairingGrace)
	defer cancel()
	_, err := h.store.PeerMessages().AdmitControl(ctx, fingerprint)
	return err == nil
}

// admitControl binds an inbound control session's store, within the
// contact's bound; the caller holds the contact's claim.
func (h *Hub) admitControl(ctx context.Context, contact string) (messaging.ControlStore, error) {
	admitted, err := h.store.PeerMessages().AdmitControl(ctx, contact)
	if err != nil {
		return nil, err
	}
	if !h.takeControl(contact) {
		return nil, errControlTooSoon
	}
	return &noticeStore{ControlStore: admitted, contact: contact}, nil
}

// controlDone records an inbound control session: an attempt only if this
// side's notice went out.
func (h *Hub) controlDone(ctx context.Context, s messaging.ControlStore, r messaging.ControlResult) {
	if ns, ok := s.(*noticeStore); ok {
		h.controlEnded(ctx, ns.run(r), 0)
	}
}

// noticeStore is a control session's store that remembers which of this
// side's notices it offered.
type noticeStore struct {
	messaging.ControlStore
	contact string
	offered int64 // the offered notice's generation, 0 for none
}

// PendingNotice is the store's, remembered.
func (s *noticeStore) PendingNotice(ctx context.Context) (*messaging.ContactState, error) {
	n, err := s.ControlStore.PendingNotice(ctx)
	if n != nil {
		s.offered = int64(n.Gen)
	}
	return n, err
}

func (s *noticeStore) run(r messaging.ControlResult) controlRun {
	return controlRun{contact: s.contact, result: r, offered: s.offered}
}

// controlRun is what one control session with contact did.
type controlRun struct {
	contact string
	result  messaging.ControlResult
	offered int64
}

// notice is the generation the run's attempt counts on: the notice that
// went out, else scheduled.
func (r controlRun) notice(scheduled int64) int64 {
	if r.result.Sent {
		return r.offered
	}
	return scheduled
}

// controlEnded records a run's attempt and announces what it stored.
func (h *Hub) controlEnded(ctx context.Context, run controlRun, scheduled int64) {
	if gen := run.notice(scheduled); gen != 0 {
		h.recordNotice(ctx, noticeTry{contact: run.contact, gen: gen, sent: run.result.Sent})
	}
	if run.result.ClosedByUnpair {
		h.dropLive(run.contact)
	}
	if run.result.Received {
		h.PairingChanged()
	}
}

// noticeTry is one attempt at the contact's notice of generation gen.
type noticeTry struct {
	contact string
	gen     int64
	sent    bool
}

// recordNotice counts the attempt and schedules the next by the hub's
// backoff.
func (h *Hub) recordNotice(ctx context.Context, t noticeTry) {
	storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), PairingGrace)
	defer cancel()
	root := h.store.PeerMessages()
	n, err := root.Notice(storeCtx, t.contact, t.gen)
	if err != nil {
		h.log.Warn("contact notice unreadable", "err", err)
		return
	}
	outcome := store.NoticeFailed
	if t.sent {
		outcome = store.NoticeSent
	}
	now := h.pair.cfg.Clock()
	if _, err := root.RecordNoticeAttempt(storeCtx, store.NoticeAttempt{Contact: t.contact, Gen: t.gen, Outcome: outcome,
		At: now, Next: now.Add(backoff(n.Attempts + 1))}); err != nil {
		h.log.Warn("contact notice attempt not recorded", "err", err)
	}
}

// refusedAtTLS: a message dial the peer refused at its handshake, as it
// refuses a contact it paused or unpaired.
func refusedAtTLS(err error) bool {
	if err == nil {
		return false
	}
	class := classifySync(err)
	return class == SyncRefused || class == SyncPinMismatch
}

// remotePauses is a bound store that clears the peer's stored pause.
type remotePauses interface {
	ClearRemotePaused(ctx context.Context) error
}

// ClearRemotePause clears the peer's stored pause once a full message
// session of PairingVersion with it succeeded, either role: the peer
// admitted this side, so it is no longer paused.
func ClearRemotePause(ctx context.Context, s messaging.Store, version byte) error {
	clearer, ok := s.(remotePauses)
	if !ok || version < messaging.PairingVersion {
		return nil
	}
	return clearer.ClearRemotePaused(ctx)
}

func (h *Hub) clearRemotePause(ctx context.Context, s messaging.Store, version byte) {
	if err := ClearRemotePause(ctx, s, version); err != nil {
		h.log.Warn("remote pause not cleared", "err", err)
	}
}
