package hub

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Messages between paired workspaces (#210). Exactly one side dials each
// contact: the one holding its address, or, when both do after pairing both
// ways, the lower fingerprint. It syncs in short sessions; the other side
// only answers, and its messages leave at the dialer's next session. A
// contact has at most one session at a time, inbound or outbound.

const (
	// MessageBudget bounds one session's network work.
	MessageBudget = 30 * time.Second
	// Sync intervals: while anything is waiting or there was recent traffic,
	// and otherwise.
	MessageActiveInterval = 30 * time.Second
	MessageIdleInterval   = 5 * time.Minute
	messageRecent         = 10 * time.Minute
	messageBackoffMin     = time.Minute
	messageBackoffMax     = time.Hour
	messageCollectEvery   = time.Hour
)

// Sync failure classes.
const (
	SyncUnsupported = "unsupported" // the peer doesn't speak message sessions
	SyncUnreachable = "unreachable"
	SyncTimeout     = "timeout"
	SyncPinMismatch = "pin_mismatch"
	SyncRefused     = "refused" // the handshake failed otherwise
	SyncBusy        = "busy"    // a session with this contact was already running
	SyncProtocol    = "protocol"
	SyncClosed      = "closed" // the peer ended the session; the wire carries no reason
	SyncStore       = "store"  // this side's own store failed
	SyncOther       = "other"
)

// MessageSync is what this side knows of syncing with one contact.
type MessageSync struct {
	Dials       bool
	LastAttempt time.Time
	LastSuccess time.Time
	LastFailure string
	Failures    int
	NextAt      time.Time
	Live        bool // a live link is open: messages leave at once, either way

	dest   string    // the destination dialled
	destAt time.Time // its redemption's start: an older read never replaces it
	gen    uint64    // moves on with each move; fences dials from before it
}

// messageWiring is the hub's message sessions; safe from any goroutine.
type messageWiring struct {
	mu      sync.Mutex
	busy    map[string]bool
	syncs   map[string]*MessageSync
	traffic map[string]time.Time
	wake    chan string
	pending map[string]bool // contacts whose change the Run goroutine hasn't announced yet
	epoch   uint64          // moves on with each pairing change: dials from before read their contact again
	changed chan struct{}   // the hub's background announcement signal, shared with pairing
}

// init readies the wiring with the hub's announcement signal; New calls it.
// Later calls only make sure the maps exist.
func (w *messageWiring) init(changed ...chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(changed) > 0 {
		w.changed = changed[0]
	}
	if w.busy == nil {
		w.busy, w.syncs, w.traffic = map[string]bool{}, map[string]*MessageSync{}, map[string]time.Time{}
		w.wake, w.pending = make(chan string, 64), map[string]bool{}
	}
}

// MessagesChanged notes that a contact's messages changed durably; the Run
// goroutine announces it to the operator session. Safe from any goroutine;
// it never blocks.
func (h *Hub) MessagesChanged(contact string) {
	w := &h.msgs
	w.init()
	w.mu.Lock()
	w.pending[contact] = true
	w.mu.Unlock()
	select {
	case w.changed <- struct{}{}:
	default:
	}
}

// announceMessages emits one contact_messages_changed per contact noted
// since the last announcement; it runs on the Run goroutine.
func (h *Hub) announceMessages() {
	w := &h.msgs
	w.mu.Lock()
	contacts := make([]string, 0, len(w.pending))
	for c := range w.pending {
		contacts = append(contacts, c)
	}
	clear(w.pending)
	w.mu.Unlock()
	slices.Sort(contacts)
	for _, c := range contacts {
		h.emit(protocol.SessionEvent{Kind: protocol.EventContactMessagesChanged, ContactKey: c})
	}
}

// messagePins admits a message session only from a confirmed contact this
// side hasn't paused (#276).
func (h *Hub) messagePins(fingerprint string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), PairingGrace)
	defer cancel()
	_, err := h.admit(ctx, fingerprint)
	return err == nil
}

// beginMessages claims a contact for one session; end releases it,
// recording whether anything moved.
func (h *Hub) beginMessages(contact string) (end func(moved bool), ok bool) {
	w := &h.msgs
	w.init()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.busy[contact] {
		return nil, false
	}
	w.busy[contact] = true
	return func(moved bool) {
		w.mu.Lock()
		delete(w.busy, contact)
		if moved {
			w.traffic[contact] = h.pair.cfg.Clock()
		}
		w.mu.Unlock()
		if moved {
			h.MessagesChanged(contact)
			h.importsDue() // it may have staged messages for agents
		}
	}, true
}

// SyncMessages asks for a session with contact soon: after a message is
// queued, on the operator's request, or at start. Safe from any goroutine;
// it never blocks.
func (h *Hub) SyncMessages(contact string) {
	h.msgs.init()
	if h.wakeLive(contact) {
		return
	}
	select {
	case h.msgs.wake <- contact:
	default: // a full queue syncs on its next tick anyway
	}
}

// MessageSyncStatus is the sync state of one contact.
func (h *Hub) MessageSyncStatus(contact string) MessageSync {
	w := &h.msgs
	w.init()
	w.mu.Lock()
	defer w.mu.Unlock()
	s := MessageSync{}
	if got := w.syncs[contact]; got != nil {
		s = *got
	}
	s.Live = h.Live(contact)
	return s
}

// dials: this side holds the contact's address, and, if both sides do,
// has the lower fingerprint.
func dials(c peer.Contact, own string) bool {
	return c.Destination != "" && (!c.Invited || own < c.IdentityKey)
}

// syncClasses name why a session failed, first match wins: this side's
// store, a deadline, the dial, then the version, identity, framing and the
// peer closing.
var syncClasses = []struct {
	class string
	match func(error) bool
}{
	{SyncStore, func(err error) bool { return errors.As(err, new(messaging.StoreError)) }},
	{SyncTimeout, timedOut}, // a dial that timed out is a timeout, not merely unreachable
	{SyncUnreachable, func(err error) bool { return errors.As(err, new(errUnreachable)) }},
	{SyncUnsupported, func(err error) bool {
		return firstcontact.NoCommonProtocol(err) || errors.Is(err, messaging.ErrUnsupported)
	}},
	{SyncPinMismatch, func(err error) bool { return errors.Is(err, peer.ErrPeerMismatch) }},
	{SyncProtocol, func(err error) bool {
		return errors.Is(err, messaging.ErrProtocol) || errors.Is(err, peer.ErrPairingProtocol)
	}},
	{SyncClosed, closedByPeer},
	{SyncRefused, func(err error) bool { return errors.Is(err, firstcontact.ErrFirstContact) }},
}

// classifySync names why a session failed.
func classifySync(err error) string {
	for _, c := range syncClasses {
		if c.match(err) {
			return c.class
		}
	}
	return SyncOther
}

// timedOut: a deadline, of the context or of the connection.
func timedOut(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout()
}

// closedByPeer: the connection ended under the session.
func closedByPeer(err error) bool {
	for _, end := range []error{io.EOF, io.ErrUnexpectedEOF, syscall.ECONNRESET, syscall.EPIPE, net.ErrClosed} {
		if errors.Is(err, end) {
			return true
		}
	}
	return false
}

// backoff is the wait after failures in a row: from a minute, doubling, up
// to an hour, with up to a fifth of jitter either way.
func backoff(failures int) time.Duration {
	wait := messageBackoffMin << min(failures-1, 6)
	wait = min(wait, messageBackoffMax)
	jitter := time.Duration(rand.Int64N(int64(wait)/5*2+1)) - wait/5
	return wait + jitter
}
