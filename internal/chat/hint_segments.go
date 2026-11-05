package chat

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// A ⇄ tab's audience line (#313) is styled in parts, as the local tabs show
// theirs: who the draft goes to in the audience colour, the notes beside it
// quiet, and why it is held in the error colour.

// hintSegment is one part of the audience line and its style.
type hintSegment struct {
	text  string
	style lipgloss.Style
}

// hintSegments is the audience line in parts: a ⇄ tab's styled by part,
// any other one style throughout.
func (m *Model) hintSegments() []hintSegment {
	if key := m.conv.selected; key.isContact() && m.compose.cmd == nil {
		return m.contactSegments(key)
	}
	text := m.hintText()
	return []hintSegment{{text, m.audienceStyle(text)}}
}

// plainSegments is the parts as plain text.
func plainSegments(segs []hintSegment) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.text)
	}
	return b.String()
}

// renderSegments styles each part, cut to width cells with a visible
// ellipsis where the line runs over.
func renderSegments(segs []hintSegment, width int) string {
	var b strings.Builder
	left := width
	for _, s := range segs {
		w := ansi.StringWidth(s.text)
		if w > left {
			b.WriteString(s.style.Render(ansi.Truncate(s.text, left, "…")))
			break
		}
		b.WriteString(s.style.Render(s.text))
		left -= w
	}
	return b.String()
}
