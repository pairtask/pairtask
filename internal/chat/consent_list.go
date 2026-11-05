package chat

import (
	"fmt"
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

// A contact's held requests as a list (#293): /pair consent contact:NAME
// opens it, or a Needs you entry names that command. A row is only
// highlighted; Enter previews it with no choice selected, and Esc in the
// preview comes back here. Nothing in the list chooses or decides.

// consentStaleNote: the connection was refreshed under an open preview.
const consentStaleNote = "the connection was refreshed: Esc, then /pair consent again to decide on what the broker holds now"

// consentListKey is every key while the list shows: move, preview, close.
func (m *Model) consentListKey(key string) {
	switch key {
	case "esc":
		m.closeConsent()
	case "up", "down":
		m.moveConsentRow(map[string]int{"up": -1, "down": 1}[key])
	case "pgup", "pgdown":
		m.scrollConsent(consentScrollKeys[key])
	case "enter":
		m.previewListed()
	}
}

// moveConsentRow highlights the row above or below.
func (m *Model) moveConsentRow(step int) {
	l := m.consent.list
	l.row = max(0, min(l.row+step, len(l.items)-1))
}

// previewListed previews the highlighted request, unless the list is being
// read again or predates a reconnection.
func (m *Model) previewListed() {
	c := m.consent
	if c.refreshing || c.stale {
		m.status = "the list is out of date; wait for it to be read again, or Esc and /pair consent again"
		return
	}
	m.showConsentView(c.list.items[c.list.row])
}

// backFromPreview: Esc in a preview returns to the list it came from, or
// closes it. Either way nothing is decided.
func (m *Model) backFromPreview() {
	c := m.consent
	if c.list == nil || c.sending {
		m.closeConsent()
		return
	}
	c.list.previewing, c.selected = false, consentNoSelection
	m.panel = &panel{kind: panelConsent}
	m.status = "nothing decided; ↑/↓ highlight · Enter previews · Esc closes"
}

// consentListRows are the list: whose requests, one row each, and the keys.
func (m *Model) consentListRows() []string {
	c := m.consent
	l := c.list
	title := fmt.Sprintf("Held requests from %s (%d)", safe(m.contactTitle(contactKey(l.contact))), len(l.items))
	rows := []string{titleStyle.Render(title) + mutedStyle.Render("  (Esc closes; nothing is decided here)")}
	rows = append(rows, m.consentNoteRows()...)
	for i, v := range l.items {
		mark := "  "
		if i == l.row {
			mark = "▸ "
		}
		rows = append(rows, mark+safe(v.ID)+"  "+consentAsks(v)+mutedStyle.Render("  expires "+when(v.ExpiresAt)))
	}
	return append(rows, "", mutedStyle.Render("  ↑/↓ highlight · Enter previews it, then 1–5 and Ctrl+Y decide · Esc closes"))
}

// consentAsks is what a held request asks, in short: each target and its
// operations.
func consentAsks(v protocol.ConsentView) string {
	parts := make([]string, 0, len(v.Targets))
	for _, t := range v.Targets {
		parts = append(parts, safe(t.Agent)+": "+safe(strings.Join(t.Operations, ", ")))
	}
	return strings.Join(parts, "; ")
}

// consentNoteRows say why what is shown can't be chosen on now.
func (m *Model) consentNoteRows() []string {
	c := m.consent
	switch {
	case c.stale:
		return []string{warnStyle.Render("  " + consentStaleNote)}
	case c.refreshing:
		return []string{mutedStyle.Render("  changed; reading it again…")}
	}
	return nil
}
