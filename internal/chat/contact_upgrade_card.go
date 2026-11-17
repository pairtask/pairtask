package chat

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// pairtask's notice to a workspace that can't sign (#395) shows as a card.
// Only its sender and first line are read: the card is drawn from a fixed
// text here, so a workspace that forges one can't put words in it. An older
// chat shows the notice's own text instead.

const upgradeSteps = "Update pairtask, then run `pairtask identity init` once on your host."

// upgradeCard is the card for v when it is that notice, else nil.
func (m *Model) upgradeCard(c protocol.PairingContactView, v protocol.ContactMessageView) []string {
	if !upgradeNotice(v) {
		return nil
	}
	ws := safe(contactDisplay(c))
	if v.Direction == "out" {
		return m.cardRows(mutedStyle, "Sent automatically to "+ws+": how to update so messages can be signed",
			"They run a version without signatures; this notice tells them what to do.")
	}
	return m.cardRows(alertTitle, ws+" asks you to update pairtask so messages can be signed",
		"Your version can't sign messages, so neither side can verify who sent what.",
		upgradeSteps, "How it works: docs/pairing.md#signed-messages")
}

// upgradeNotice: v is pairtask's signing notice, by its sender and first line.
func upgradeNotice(v protocol.ContactMessageView) bool {
	first, _, _ := strings.Cut(v.Text, "\n")
	return v.From == protocol.SystemSenderID && first == protocol.SigningNoticeMarker
}

// cardRows is a card in its red frame: the title in its style, then each
// line, all wrapped at the tab's width and indented as a body.
func (m *Model) cardRows(style lipgloss.Style, title string, lines ...string) []string {
	width := m.transcriptWidth() - len(body) - 2
	var rows []string
	for i, part := range wrap(title, width) {
		rows = append(rows, m.cardRow(i == 0, len(lines) == 0, style.Render(part)))
	}
	for i, line := range lines {
		parts := wrap(line, width)
		for j, part := range parts {
			rows = append(rows, m.cardRow(false, i == len(lines)-1 && j == len(parts)-1, part))
		}
	}
	return rows
}

// cardRow is one row of the frame: its edge, then its text.
func (m *Model) cardRow(first, last bool, text string) string {
	edge := "│ "
	switch {
	case first:
		edge = "┌ "
	case last:
		edge = "└ "
	}
	return string(body) + alertEdge.Render(edge) + text
}
