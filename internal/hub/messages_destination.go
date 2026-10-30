package hub

import (
	"context"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

// A contact moves when a newer redemption reaches the same identity at
// another address. The message worker dials the address it last took.
// A dial records under a token: its contact's generation, which moves on
// only when the contact moves, and the pairing epoch, which moves on with
// every pairing change. A dial from before a move records nothing, and
// opens no live link. One from before a pairing change first reads the
// contact again: unmoved, it records and carries the new epoch on.

// dialToken is what one dial, or the link it opened, records under.
type dialToken struct {
	gen, epoch uint64
}

// syncOutcome is how one session, or one round of a live link, ended.
type syncOutcome struct {
	token   dialToken
	failure string // "" when it got through
	active  bool   // anything still waiting, or recent traffic
}

// recordDial records how a dial, or the link it opened, ended, unless the
// contact moved since; it reports whether it did.
func (h *Hub) recordDial(contact string, o syncOutcome) bool {
	return h.recordUnder(contact, o, &o.token)
}

// recordUnder is recordDial carrying token on: a pairing change that
// didn't move the contact renews its epoch, so a live link keeps recording.
func (h *Hub) recordUnder(contact string, o syncOutcome, token *dialToken) bool {
	for {
		o.token = *token
		epoch, done, recorded := h.tryRecord(contact, o)
		if done {
			return recorded
		}
		if !h.readDestination(contact) {
			return false
		}
		token.epoch = epoch
	}
}

// tryRecord records an outcome if token is current: done unless only the
// epoch moved on, which is then returned to read the contact under.
func (h *Hub) tryRecord(contact string, o syncOutcome) (epoch uint64, done, recorded bool) {
	w := &h.msgs
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.syncOf(contact)
	switch {
	case s.gen != o.token.gen:
		return 0, true, false
	case w.epoch != o.token.epoch:
		return w.epoch, false, false
	}
	h.recordOn(s, o.failure, o.active)
	return 0, true, true
}

// readDestination adopts contact's stored destination; a move then fences
// the dial that asked.
func (h *Hub) readDestination(contact string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), PairingGrace)
	defer cancel()
	c, found, err := h.store.Contact(ctx, contact)
	if err != nil || !found {
		return false
	}
	h.destinationMoved(c)
	return true
}

// recordRound records a completed round on a live link: one the contact
// dialled has no dial of ours to fence.
func (h *Hub) recordRound(l *liveLink) {
	if l.serving {
		h.recordSync(l.contact, "", true)
		return
	}
	h.recordUnder(l.contact, syncOutcome{active: true}, &l.token)
}

// adopt takes c's destination, unless the read is older than the one s
// dials: current reports that s dials c's destination now, moved that it
// replaced another, whose failures it then forgets.
func (s *MessageSync) adopt(c peer.Contact) (current, moved bool) {
	switch {
	case c.Destination == s.dest:
		if c.DestinationAt.After(s.destAt) {
			s.destAt = c.DestinationAt
		}
		return true, false
	case s.dest != "" && !c.DestinationAt.After(s.destAt):
		return false, false // read before the move
	}
	moved = s.dest != ""
	s.dest, s.destAt = c.Destination, c.DestinationAt
	if moved {
		s.gen++
		s.Failures, s.LastFailure, s.NextAt = 0, "", time.Time{}
	}
	return true, moved
}

// syncOf is contact's sync state, made on first use; the caller holds w.mu.
func (w *messageWiring) syncOf(contact string) *MessageSync {
	s := w.syncs[contact]
	if s == nil {
		s = &MessageSync{}
		w.syncs[contact] = s
	}
	return s
}

// fenceDials is a pairing change's part in message sync: a dial running
// now reads its contact again before it records, and each contact's
// destination is checked again.
func (h *Hub) fenceDials() {
	w := &h.msgs
	w.init()
	w.mu.Lock()
	w.epoch++
	w.mu.Unlock()
	if h.store == nil {
		return
	}
	h.pair.workers.Add(1)
	go func() {
		defer h.pair.workers.Done()
		h.checkDestinations()
	}()
}

// checkDestinations adopts each contact's stored destination: one that
// moved drops its link to the old address and syncs with the new one.
func (h *Hub) checkDestinations() {
	ctx, cancel := context.WithTimeout(context.Background(), PairingGrace)
	defer cancel()
	contacts, err := h.store.Contacts(ctx)
	if err != nil {
		h.log.Warn("message sync: contacts unreadable", "err", err)
		return
	}
	for _, c := range contacts {
		h.destinationMoved(c)
	}
}

// destinationMoved adopts the destination of a contact this side dialled;
// whichever path sees a move first drops the old address's link.
func (h *Hub) destinationMoved(c peer.Contact) {
	w := &h.msgs
	w.mu.Lock()
	s := w.syncs[c.IdentityKey]
	moved := false
	if s != nil { // never dialled: its first dial takes the address
		_, moved = s.adopt(c)
	}
	w.mu.Unlock()
	if moved {
		h.leaveAddress(c.IdentityKey, true)
	}
}

// leaveAddress drops what belonged to a moved contact's old address: its
// live link, which redials as it ends, and its redial backoff. wake syncs
// at once when there was no link to end.
func (h *Hub) leaveAddress(contact string, wake bool) {
	h.log.Info("contact moved; redialling", "contact", contact)
	h.resetLiveBackoff(contact)
	if !h.haltDialer(contact) && wake {
		h.SyncMessages(contact)
	}
}

// resetLiveBackoff forgets contact's live link failures in a row.
func (h *Hub) resetLiveBackoff(contact string) {
	w := &h.live
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ready()
	delete(w.failures, contact)
}

// haltDialer stops this side's live link to contact, if it dialled one.
func (h *Hub) haltDialer(contact string) bool {
	w := &h.live
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ready()
	l := w.links[contact]
	if l == nil || l.serving {
		return false
	}
	l.halt()
	return true
}

// dropLink closes a link a fenced dial opened, before it carries anything.
func (h *Hub) dropLink(contact string, d *dialedLink) {
	if d == nil {
		return
	}
	h.releaseLive(contact, d.link)
	d.conn.Close()
}
