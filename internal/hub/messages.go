package hub

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/peer/messaging"
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
	SyncStore       = "store"
)

// MessageSync is what this side knows of syncing with one contact.
type MessageSync struct {
	Dials       bool
	LastAttempt time.Time
	LastSuccess time.Time
	LastFailure string
	Failures    int
	NextAt      time.Time
}

// messageWiring is the hub's message sessions; safe from any goroutine.
type messageWiring struct {
	mu      sync.Mutex
	busy    map[string]bool
	syncs   map[string]*MessageSync
	traffic map[string]time.Time
	wake    chan string
}

func (w *messageWiring) init() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.busy == nil {
		w.busy, w.syncs, w.traffic = map[string]bool{}, map[string]*MessageSync{}, map[string]time.Time{}
		w.wake = make(chan string, 64)
	}
}

// messagePins admits a message session only from a confirmed contact.
func (h *Hub) messagePins(fingerprint string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), PairingGrace)
	defer cancel()
	ok, err := h.store.PeerMessages().Confirmed(ctx, fingerprint)
	return err == nil && ok
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
	}, true
}

// SyncMessages asks for a session with contact soon: after a message is
// queued, on the operator's request, or at start. Safe from any goroutine;
// it never blocks.
func (h *Hub) SyncMessages(contact string) {
	h.msgs.init()
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
	if s := w.syncs[contact]; s != nil {
		return *s
	}
	return MessageSync{}
}

// dials: this side holds the contact's address, and, if both sides do,
// has the lower fingerprint.
func dials(c peer.Contact, own string) bool {
	return c.Destination != "" && (!c.Invited || own < c.IdentityKey)
}

// classifySync names why a session failed.
func classifySync(err error) string {
	switch {
	case firstcontact.NoCommonProtocol(err), errors.Is(err, messaging.ErrUnsupported):
		return SyncUnsupported
	case errors.Is(err, peer.ErrPeerMismatch):
		return SyncPinMismatch
	case errors.Is(err, context.DeadlineExceeded):
		return SyncTimeout
	case errors.As(err, new(errUnreachable)):
		return SyncUnreachable
	case errors.Is(err, messaging.ErrProtocol):
		return SyncProtocol
	case errors.Is(err, firstcontact.ErrFirstContact):
		return SyncRefused
	}
	return SyncStore
}

// backoff is the wait after failures in a row: from a minute, doubling, up
// to an hour, with up to a fifth of jitter either way.
func backoff(failures int) time.Duration {
	wait := messageBackoffMin << min(failures-1, 6)
	wait = min(wait, messageBackoffMax)
	jitter := time.Duration(rand.Int64N(int64(wait)/5*2+1)) - wait/5
	return wait + jitter
}
