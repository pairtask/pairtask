package hub

import (
	"context"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Finding colleagues (#284): the host helper reports, with each lease it
// asks for, the named invitations it hears on the local network, the online
// tailnet devices, and whether the announcement asked of it left the host.
// The broker keeps only the latest report, in memory, and serves it to
// /pair find; a report older than DiscoveryHelperStale counts as nothing.
// Every report replaces the whole snapshot, so what the helper no longer
// hears disappears with its next request.

// FindWindow is how long after a /pair find the lease keeps asking the
// helper to browse for invitations and read the tailnet.
const FindWindow = 2 * time.Minute

// findState is the helper's latest report and the operator's interest.
type findState struct {
	mu          sync.Mutex
	seen        time.Time // the helper's last lease request
	found       []protocol.FoundInvitation
	devices     []protocol.TailnetDevice
	announcing  *protocol.Announcing
	wantedUntil time.Time // /pair find keeps the helper looking until then
}

// helperSeen records a lease request and takes its report whole.
func (f *findState) helperSeen(msg protocol.Message, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = now
	f.found = append([]protocol.FoundInvitation{}, msg.ContactFound...)
	f.devices = append([]protocol.TailnetDevice{}, msg.ContactDevices...)
	f.announcing = nil
	if msg.ContactAnnouncing != nil {
		a := *msg.ContactAnnouncing
		f.announcing = &a
	}
}

// helperLive: the helper asked for a lease within DiscoveryHelperStale.
func (f *findState) helperLive(now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.seen.IsZero() && now.Sub(f.seen) < protocol.DiscoveryHelperStale
}

// want keeps the helper looking for FindWindow from now; the panel's
// re-reads within a window don't extend it, so one find is one window.
func (f *findState) want(now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if now.After(f.wantedUntil) {
		f.wantedUntil = now.Add(FindWindow)
	}
}

func (f *findState) wanted(now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return now.Before(f.wantedUntil)
}

// snapshot is find's answer: nothing listed from a stale helper.
func (f *findState) snapshot(now time.Time) protocol.ContactFind {
	f.mu.Lock()
	defer f.mu.Unlock()
	view := protocol.ContactFind{Invitations: []protocol.FoundInvitation{}, Devices: []protocol.TailnetDevice{}}
	if f.seen.IsZero() {
		return view
	}
	seen := f.seen
	view.HelperSeenAt = &seen
	if now.Sub(f.seen) >= protocol.DiscoveryHelperStale {
		return view
	}
	view.Invitations = append(view.Invitations, f.found...)
	view.Devices = append(view.Devices, f.devices...)
	return view
}

// announceState is what the helper last said about announcing the
// invitation id; a report on another invitation, a replaced one with the
// same name included, says nothing about this one.
func (f *findState) announceState(id string, now time.Time) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.seen.IsZero() || now.Sub(f.seen) >= protocol.DiscoveryHelperStale:
		return protocol.AnnounceHelperLost
	case f.announcing == nil || f.announcing.ID != id:
		return protocol.AnnounceRequested
	case f.announcing.Sent:
		return protocol.AnnounceAnnouncing
	}
	return protocol.AnnounceFailed
}

// operatorFind answers /pair find from the latest report and asks the
// helper to keep looking.
func (h *Hub) operatorFind(_ context.Context, msg protocol.Message) protocol.Message {
	now := h.contactNow()
	h.find.want(now)
	view := h.find.snapshot(now)
	return protocol.Message{Type: protocol.TypeOperatorContactResult, MessageID: msg.MessageID, ContactOp: msg.ContactOp, ContactFind: &view}
}

// discoverableInvitation is the lease's announcement: the open, unconsumed
// discoverable invitation under this workspace's name, or nil.
func (h *Hub) discoverableInvitation() *protocol.DiscoveryInvitation {
	status, ok := h.pairing.Status()
	if !ok || !status.Discoverable || status.Consumed || status.Expired {
		return nil
	}
	return &protocol.DiscoveryInvitation{ID: status.ID, Name: h.pair.cfg.Name, ExpiresAt: status.ExpiresAt}
}

// discoverableRefusal says why a discoverable invitation can't be opened
// now: this workspace has no name of its own, or no helper is asking for
// leases; nothing is opened then.
func (h *Hub) discoverableRefusal() *routingError {
	if !protocol.ValidDisplayName(h.pair.cfg.Name) || h.pair.cfg.Name == peer.DefaultPairingName {
		return &routingError{protocol.ErrPairing, "name this workspace first: pairtask pairing on --name NAME"}
	}
	if !h.find.helperLive(h.contactNow()) {
		return &routingError{protocol.ErrPairing, "the discovery helper is off or not installed: pairtask pairing on --discovery"}
	}
	return nil
}

// invitationView adds the announcement to the open invitation's view.
func (h *Hub) invitationView(view *protocol.InvitationView, discoverable bool) {
	if !discoverable {
		return
	}
	view.Discoverable, view.PublicName = true, h.pair.cfg.Name
	if !view.Consumed && !view.Expired {
		view.AnnounceState = h.find.announceState(view.ID, h.contactNow())
	}
}
