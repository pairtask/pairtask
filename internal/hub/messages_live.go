package hub

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

// Live links (#222): after its first round a session between two workspaces
// that both speak messaging.LiveVersion stays open, so a message leaves the
// moment it is queued on either side. Each side keeps at most liveLinks open
// each way, under the relays' 16 connections, so pairing and first rounds
// keep room. A link is the contact's only one: the dialer opens no second,
// and the dialed side lets a reconnection replace a link it still holds.

const (
	liveLinks = 12
	// LiveHeartbeat is the dialer's ping while idle, under the relays' 30 s
	// idle limit.
	LiveHeartbeat = 15 * time.Second
	// liveRotate is what a link carries either way before the dialer
	// reconnects: with a round's worst case on top it stays under the
	// relays' 1 MiB per direction.
	liveRotate     = 384 << 10
	liveMaxAge     = time.Hour
	liveFirstRetry = time.Second // a link that drops is redialled within this
	liveBackoffMax = time.Minute
)

func liveTiming() messaging.LinkTiming {
	return messaging.LinkTiming{Round: MessageBudget, Grace: PairingGrace, Heartbeat: LiveHeartbeat,
		Rotate: liveRotate, MaxAge: liveMaxAge}
}

// liveLink is one open link's handle: its contact, its wake token and its
// stop.
type liveLink struct {
	contact  string
	wake     chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
	serving  bool
	token    dialToken // a dialled link's: what its end records under
}

func (l *liveLink) halt() { l.stopOnce.Do(func() { close(l.stop) }) }

// liveWiring is the hub's open links; safe from any goroutine.
type liveWiring struct {
	mu       sync.Mutex
	links    map[string]*liveLink
	dialing  int
	serving  int
	failures map[string]int
}

func (w *liveWiring) ready() {
	if w.links == nil {
		w.links, w.failures = map[string]*liveLink{}, map[string]int{}
	}
}

// claimLive claims a link for contact. The dialing side never opens a
// second; the dialed side replaces one still open, since only a dialer that
// lost its link reconnects. Either side needs a free slot.
func (h *Hub) claimLive(contact string, serving bool) (*liveLink, bool) {
	w := &h.live
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ready()
	old := w.links[contact]
	if old != nil && !serving {
		return nil, false
	}
	count := &w.dialing
	if serving {
		count = &w.serving
	}
	// A replacement reuses the capacity of the link it halts.
	if old == nil && *count >= liveLinks {
		return nil, false
	}
	*count++
	if old != nil {
		old.halt()
	}
	l := &liveLink{contact: contact, wake: make(chan struct{}, 1), stop: make(chan struct{}), serving: serving}
	w.links[contact] = l
	return l, true
}

// releaseLive frees a link's slot, and its contact unless a newer link
// replaced it, then tells the operator, so a reload sees the registry as it
// now is.
func (h *Hub) releaseLive(contact string, l *liveLink) {
	w := &h.live
	w.mu.Lock()
	if l.serving {
		w.serving--
	} else {
		w.dialing--
	}
	if w.links[contact] == l {
		delete(w.links, contact)
	}
	w.mu.Unlock()
	h.MessagesChanged(contact)
}

// wakeLive hands a token to contact's open link, if there is one.
func (h *Hub) wakeLive(contact string) bool {
	w := &h.live
	w.mu.Lock()
	defer w.mu.Unlock()
	l := w.links[contact]
	if l == nil {
		return false
	}
	select {
	case l.wake <- struct{}{}:
	default: // a token is waiting already
	}
	return true
}

// Live reports whether a link with contact is open.
func (h *Hub) Live(contact string) bool {
	w := &h.live
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.links[contact] != nil
}

// FirstRound is what a link's first round settled: what it moved, counted
// towards rotation, and the version both sides agreed, which every later
// round keeps.
type FirstRound struct {
	Carried *messaging.Counted
	Version byte
	// Store is the store the first round was admitted with (#276): the link
	// goes on with it, never with another.
	Store messaging.Store
}

// link is a contact's live link over a connection's frames.
func (h *Hub) link(open func(context.Context) messaging.Frames, l *liveLink, first FirstRound) *messaging.Link {
	contact := l.contact
	link := &messaging.Link{
		Session: messaging.Session{Store: first.Store, Contact: contact, Now: h.pair.cfg.Clock, Version: first.Version},
		Open:    open,
		Wake:    l.wake,
		Begin: func() (func(messaging.Result, error), bool) {
			end, ok := h.beginMessages(contact)
			if !ok {
				return nil, false
			}
			return func(r messaging.Result, err error) {
				end(r.Moved())
				if err == nil {
					h.recordRound(l) // LastSuccess is a completed round
				}
			}, true
		},
		Confirmed: func() bool { return fenceHolds(first.Store) },
		More:      func() bool { return h.hasOutgoing(l) },
		Timing:    liveTiming(),
	}
	if c := first.Carried; c != nil {
		link.Carried.Sent, link.Carried.Recv = c.SentBytes, c.RecvBytes
	}
	return link
}

// fenceHolds: the link's admitted store is still at its contact's
// generation; a link without one ends before its first round.
func fenceHolds(s messaging.Store) bool {
	f, ok := s.(interface{ FenceHolds(context.Context) bool })
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), PairingGrace)
	defer cancel()
	return f.FenceHolds(ctx)
}

// dropLive halts the contact's live link, whichever side dialled it: after
// a pause, the fence already keeps it from changing anything (#276).
func (h *Hub) dropLive(contact string) {
	w := &h.live
	w.mu.Lock()
	l := w.links[contact]
	w.mu.Unlock()
	if l != nil {
		l.halt()
	}
}

// hasOutgoing: messages or read receipts still to send over the link.
func (h *Hub) hasOutgoing(l *liveLink) bool {
	ctx, cancel := context.WithTimeout(context.Background(), PairingGrace)
	defer cancel()
	return outbox{h.store.PeerMessages(), l.contact, h.pair.cfg.Clock()}.waiting(ctx)
}

// outbox is what this side still has to send one contact.
type outbox struct {
	s       *store.PeerMessages
	contact string
	now     time.Time
}

// waiting: messages, read receipts or review outcomes still to send.
func (o outbox) waiting(ctx context.Context) bool {
	queued, err := o.s.Offer(ctx, o.contact, o.now, messaging.Limit{Count: 1, Bytes: messaging.SessionBytes})
	if err == nil && len(queued) > 0 {
		return true
	}
	if reads, err := o.s.PendingReads(ctx, o.contact, 1); err == nil && len(reads) > 0 {
		return true
	}
	outcomes, err := o.s.PendingOutcomes(ctx, o.contact, 1)
	return err == nil && len(outcomes) > 0
}

// runLink keeps a link until it ends or is stopped. Its opening and its
// end are news to the operator: messages leave at once only while it's up.
func (h *Hub) runLink(ctx context.Context, l *liveLink, run func(context.Context) error) error {
	h.MessagesChanged(l.contact) // its end is announced by releaseLive, once the registry says so
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-l.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	return run(ctx)
}

// dialedLink is a connection whose first round agreed to stay open.
type dialedLink struct {
	conn  *firstcontact.Conn
	link  *liveLink
	first FirstRound
}

// goLive keeps a dialed link in its own goroutine, so the sync slot that
// opened it is free again, and reconnects when it ends. A round got
// through, so the contact's reconnection backoff starts again.
func (h *Hub) goLive(ctx context.Context, d *dialedLink) {
	w := &h.live
	w.mu.Lock()
	w.ready()
	delete(w.failures, d.link.contact)
	w.mu.Unlock()
	h.pair.workers.Add(1)
	go func() {
		defer h.pair.workers.Done()
		defer d.conn.Close()
		open := func(ctx context.Context) messaging.Frames { return d.conn.MessageFrames(ctx) }
		err := h.runLink(ctx, d.link, h.link(open, d.link, d.first).RunDialer)
		h.releaseLive(d.link.contact, d.link)
		h.afterLink(ctx, d.link, err)
	}()
}

// afterLink reconnects: at once after a rotation, within a second after the
// first drop, then backing off up to a minute.
func (h *Hub) afterLink(ctx context.Context, l *liveLink, err error) {
	contact := l.contact
	if ctx.Err() != nil {
		return
	}
	if errors.Is(err, messaging.ErrRotate) {
		h.SyncMessages(contact)
		return
	}
	h.log.Info("message link ended", "class", classifySync(err))
	if !h.recordDial(contact, syncOutcome{token: l.token, failure: classifySync(err), active: true}) {
		h.SyncMessages(contact) // halted or fenced by a pairing change: redial at once, where the contact is now
		return
	}
	time.AfterFunc(h.liveDelay(contact), func() { h.SyncMessages(contact) })
}

// liveDelay is the wait before redialling after failures in a row, with up
// to a fifth of jitter either way after the first, never above the maximum.
func (h *Hub) liveDelay(contact string) time.Duration {
	w := &h.live
	w.mu.Lock()
	w.ready()
	w.failures[contact]++
	failures := w.failures[contact]
	w.mu.Unlock()
	if failures == 1 {
		return time.Duration(rand.Int64N(int64(liveFirstRetry)))
	}
	wait := min(liveFirstRetry<<min(failures-1, 6), liveBackoffMax)
	return min(wait+time.Duration(rand.Int64N(int64(wait)/5*2+1))-wait/5, liveBackoffMax)
}
