package hub

import (
	"math/rand/v2"
	"time"
)

// Quick redials of a link that dropped: at most one scheduled per contact,
// at most liveRetriesMax after a drop, then the poll's own backoff. A link
// coming back cancels the one scheduled; a stale timer firing late finds
// itself replaced and does nothing.

// scheduled is one quick redial waiting: which, and how to cancel it.
type scheduled struct {
	contact string
	gen     uint64
	timer   stopper
}

type stopper interface{ Stop() bool }

// cancelRedial cancels contact's scheduled quick redial, if any: a link
// came back, or the contact moved. Called with w.mu held.
func (w *liveWiring) cancelRedial(contact string) {
	if s, ok := w.pending[contact]; ok {
		s.timer.Stop()
		delete(w.pending, contact)
	}
}

// scheduleRedial schedules one quick redial of contact, backing off, unless
// one is already scheduled or the quick redials are spent; then the poll's
// own backoff decides when to dial again.
func (h *Hub) scheduleRedial(contact string) {
	w := &h.live
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ready()
	if _, waiting := w.pending[contact]; waiting || w.failures[contact] >= liveRetriesMax {
		return
	}
	w.failures[contact]++
	w.gen++
	gen := w.gen
	after := w.after
	if after == nil {
		after = func(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) }
	}
	ticket := scheduled{contact: contact, gen: gen}
	ticket.timer = after(redialWait(w.failures[contact]), func() { h.firedRedial(ticket) })
	w.pending[contact] = ticket
}

// firedRedial runs the scheduled redial t, unless it was cancelled or
// replaced meanwhile.
func (h *Hub) firedRedial(t scheduled) {
	w := &h.live
	w.mu.Lock()
	current := w.pending[t.contact].gen == t.gen
	if current {
		delete(w.pending, t.contact)
	}
	w.mu.Unlock()
	if current {
		h.SyncMessages(t.contact)
	}
}

// redialWait is the wait before redialling after failures in a row, with up
// to a fifth of jitter either way after the first, never above the maximum.
func redialWait(failures int) time.Duration {
	if failures == 1 {
		return time.Duration(rand.Int64N(int64(liveFirstRetry)))
	}
	wait := min(liveFirstRetry<<min(failures-1, 6), liveBackoffMax)
	return min(wait+time.Duration(rand.Int64N(int64(wait)/5*2+1))-wait/5, liveBackoffMax)
}
