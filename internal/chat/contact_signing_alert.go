package chat

import (
	"charm.land/lipgloss/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// A workspace on a version without signatures (#115): its tab carries a
// red card for as long as that holds, and /pair status says it too. Still
// to update, its messages arrive unsigned whatever this side requires;
// gone back after it signed, they're refused while signatures are required.

var (
	alertEdge  = lipgloss.NewStyle().Foreground(lipgloss.Color("167"))
	alertTitle = lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Bold(true)
)

// signingAlert is the alert's title, what it means and what to do: what
// the operator's switch does now as its footer. Empty title: no alert.
type signingAlert struct{ title, body, footer string }

// signingAlertOf is the contact's alert under this side's switch.
func signingAlertOf(c protocol.PairingContactView, require bool) signingAlert {
	ws := safe(contactDisplay(c))
	switch c.Signing {
	case protocol.ContactSigningUnsupported:
		footer := "nothing of theirs is refused"
		if require {
			footer = "require-seals takes effect once they update"
		}
		return signingAlert{ws + " runs a version without signatures",
			"Their messages arrive unsigned and can't be verified. Ask them to update pairtask.", footer}
	case protocol.ContactSigningDowngraded:
		footer := "their messages arrive unsigned"
		if require {
			footer = "their messages are refused while require-seals is on; /pair require-seals off takes them again, unsigned"
		}
		return signingAlert{ws + " went back to a version without signatures",
			"They signed before, so this is unexpected: check with them before trusting what arrives.", footer}
	}
	return signingAlert{}
}

// signingAlertLines are the alert drawn in its red frame, or none.
func (m *Model) signingAlertLines(c protocol.PairingContactView) []string {
	a := signingAlertOf(c, m.requiresSeals(c.IdentityKey))
	if a.title == "" {
		return nil
	}
	return []string{"",
		alertEdge.Render("┌ ") + alertTitle.Render(a.title),
		alertEdge.Render("│ ") + a.body,
		alertEdge.Render("└ ") + mutedStyle.Render(a.footer)}
}

// requiresSeals: this side refuses the contact's unsigned messages, as its
// newest page said.
func (m *Model) requiresSeals(contact string) bool {
	t := m.contacts.threads[contact]
	return t != nil && t.requireSeals
}

// signingStatusLabel is the alert's line in /pair status, or "".
func signingStatusLabel(c protocol.PairingContactView) string {
	switch c.Signing {
	case protocol.ContactSigningUnsupported:
		return "unsigned: their version can't sign; ask them to update"
	case protocol.ContactSigningDowngraded:
		return "went back to a version without signatures"
	}
	return ""
}

// requireSealsNote is what turning the switch on or off means now.
func requireSealsNote(c protocol.PairingContactView, require bool) string {
	switch {
	case require && c.Signing == protocol.ContactSigningUnsupported:
		return "require-seals on: takes effect once they update; until then their messages arrive unsigned"
	case require:
		return "this workspace's unsigned messages are refused from now on"
	}
	return "this workspace's unsigned messages are taken, marked unsigned"
}
