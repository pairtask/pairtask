package chat

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Contact conversations (#178): one per paired workspace, keyed by its full
// identity fingerprint, never by a name, so a workspace called "operator"
// or like an agent can't stand in for either. Its messages (#210) are in
// contact_messages.go.

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

// contactNotice is why the agent addressing rules don't apply in a
// contact tab: its composer sends to the workspace instead (contactSubmit).
func (m *Model) contactNotice(key viewKey) string {
	if !m.contactsAvailable() {
		return "this broker doesn't carry contact messages; upgrade it"
	}
	if !m.confirmedContact(key.contact) {
		return contactConfirming
	}
	return ""
}

// contactHint is the composer's line in a contact tab.
func (m *Model) contactHint(key viewKey) string {
	if notice := m.contactNotice(key); notice != "" {
		return "✗ " + notice
	}
	c, _ := m.contact(key.contact)
	return fmt.Sprintf("to workspace %q · queued now, it leaves at the next sync", safe(c.Name))
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
