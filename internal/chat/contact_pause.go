package chat

import (
	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Pausing a paired workspace (#276): /pair pause in its ⇄ tab blocks new
// traffic with it, both ways, keeping it, its grants and its history;
// /pair resume starts it again. The card and /pair status say so, and the
// composer keeps a draft rather than send it.

const (
	pairPause        = protocol.ContactOpPause
	pairResume       = protocol.ContactOpResume
	kindContactPause = "contact pause"
	pausedNote       = "paused by you · /pair resume"
	pausedSend       = "paused: /pair resume to send"
)

// pausedHere: this side's operator paused the contact.
func pausedHere(c protocol.PairingContactView) bool {
	return c.PausedAt != nil
}

// pauseCommand is /pair pause or /pair resume, for the contact whose tab is
// open, on a broker that serves it.
func (m *Model) pauseCommand(fields pairWords) tea.Cmd {
	key := m.conv.selected
	switch {
	case !m.session.HasCapability(protocol.CapabilityContactPause):
		m.status = "this broker can't pause a workspace; upgrade it"
		return nil
	case !key.isContact():
		m.status = "open the workspace's ⇄ tab, then /pair " + fields[0]
		return nil
	}
	msg := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: fields[0], ContactKey: key.contact, MessageID: uuid.NewString()}
	m.status = fields[0] + "…"
	return m.request(kindContactPause, msg, key.contact, fields[0])
}

// contactPauseResult takes the snapshot the operation answered with, so
// the card and /pair status change only once the broker did.
func (m *Model) contactPauseResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	op := req.ids[1]
	if resp.Type != protocol.TypeOperatorContactResult || resp.PairingResult == nil {
		m.status = op + " refused: " + refusalReason(resp)
		return nil
	}
	snapshot := *resp.PairingResult
	snapshot.Invitation = withoutCode(snapshot.Invitation)
	m.pairing.snapshot = &snapshot
	m.refreshDirectory()
	m.status = "resumed: queued messages can flow again"
	if op == pairPause {
		m.status = "paused: new traffic is blocked until /pair resume"
	}
	return nil
}

// contactSendHeld: a send from the contact's tab waits, its draft kept,
// for the reason the status line now gives.
func (m *Model) contactSendHeld(contact string) bool {
	return m.guardSend() || m.pausedSendRefused(contact)
}

// pausedSendRefused keeps the draft of a paused contact's tab and says why.
func (m *Model) pausedSendRefused(contact string) bool {
	c, ok := m.contact(contact)
	if !ok || !pausedHere(c) {
		return false
	}
	m.status = pausedSend
	return true
}
