package chat

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Contact conversations (#178): one per paired workspace, keyed by its full
// identity fingerprint, never by a name, so a workspace called "operator"
// or like an agent can't stand in for either. Messages between workspaces
// arrive in a later step: nothing is ever sent from one.

const kindPairingQuiet = "pairing quiet"

func contactKey(fingerprint string) viewKey { return viewKey{contact: fingerprint} }

func (k viewKey) isContact() bool { return k.contact != "" }

// contactLabel names a contact tab by its identity only; the peer's own
// display name, which it chose, appears in the conversation's notice.
func contactLabel(fingerprint string) string {
	short := fingerprint
	if len(short) > 15 {
		short = short[:15] + "…"
	}
	return "⇄ workspace " + safe(short)
}

// contactViews are the contacts from the latest pairing snapshot, one per
// identity.
func (m *Model) contactViews() []viewKey {
	s := m.pairing.snapshot
	if s == nil {
		return nil
	}
	keys := make([]viewKey, 0, len(s.Contacts))
	for _, c := range s.Contacts {
		keys = append(keys, contactKey(c.IdentityKey))
	}
	return keys
}

// contact is the snapshot's view of one contact.
func (m *Model) contact(fingerprint string) (protocol.PairingContactView, bool) {
	if s := m.pairing.snapshot; s != nil {
		for _, c := range s.Contacts {
			if c.IdentityKey == fingerprint {
				return c, true
			}
		}
	}
	return protocol.PairingContactView{}, false
}

// contactUnsendable is why a contact conversation's composer sends nothing.
const contactUnsendable = "messages between workspaces arrive in a later step; nothing is sent from here"

// contactNotice is the composer's refusal in a contact conversation.
func (m *Model) contactNotice(viewKey) string { return contactUnsendable }

// contactCard fills a contact conversation, most important first: who it
// is and whether both sides confirmed, why nothing can be sent, and the
// full identity to compare.
func (m *Model) contactCard(key viewKey, rows int) []string {
	c, ok := m.contact(key.contact)
	state := "confirming: the peer has not yet confirmed the decision"
	if ok && c.Confirmed {
		state = "confirmed on both sides"
	}
	lines := []string{
		titleStyle.Render(fmt.Sprintf("⇄ workspace %q", safe(c.Name))) + mutedStyle.Render(" · "+state),
		"  " + contactUnsendable,
		mutedStyle.Render("  identity " + safe(key.contact)),
	}
	return lines[:min(len(lines), rows)]
}

// refreshContacts quietly reads the pairing state after a baseline, so
// contact conversations appear without a /pair.
func (m *Model) refreshContacts() tea.Cmd {
	if !m.session.HasCapability(protocol.CapabilityPairingContacts) {
		return nil
	}
	return m.request(kindPairingQuiet, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpList},
		protocol.PairingOpList)
}

// pairingChanged reads the pairing state again after a background change:
// one quiet read at a time, and one more after it if changes kept coming.
func (m *Model) pairingChanged() tea.Cmd {
	if m.requested(kindPairingQuiet, protocol.PairingOpList) {
		m.pairing.stale = true
		return nil
	}
	return m.refreshContacts()
}

// quietPairingResult keeps the snapshot, without its code and without
// opening the panel or touching the draft.
func (m *Model) quietPairingResult(_ pendingRequest, resp protocol.Message) tea.Cmd {
	if resp.Type == protocol.TypeOperatorPairingResult && resp.PairingResult != nil {
		snapshot := *resp.PairingResult
		snapshot.Invitation = withoutCode(snapshot.Invitation)
		m.pairing.snapshot = &snapshot
		m.refreshDirectory()
	}
	if !m.pairing.stale {
		return nil
	}
	m.pairing.stale = false
	return m.refreshContacts()
}

func withoutCode(inv *protocol.InvitationView) *protocol.InvitationView {
	if inv == nil {
		return nil
	}
	copied := *inv
	copied.Code = ""
	return &copied
}
