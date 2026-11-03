package chat

import (
	"fmt"
	"os"

	"charm.land/lipgloss/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Review dots (#298): one mark for where a review or request stands, the
// same in the footer, /reviews and the ⇄ cards. Yellow and green are a
// process, waiting for the operator or under way, and blink while it
// lasts; red is a state, a failure, and stays still. Without colour each
// has its own glyph.

type reviewDot int

const (
	dotNone   reviewDot = iota
	dotGreen            // under way: running here, or asked and still open
	dotYellow           // waiting for the operator's decision
	dotRed              // failed
)

// dotColours and dotGlyphs draw each dot with colour, and without.
var (
	dotColours = map[reviewDot]string{dotGreen: "71", dotYellow: "214", dotRed: "196"}
	dotGlyphs  = map[reviewDot]string{dotGreen: "●", dotYellow: "?", dotRed: "!"}
)

// dotLook is how a dot is drawn: in a blink's dark half, a space as wide;
// without colour, by its own glyph.
type dotLook struct{ off, mono bool }

// look is how this chat draws a dot now: without colour when the operator
// asked for none (no-color.org) or the terminal has none.
func (m *Model) look(off bool) dotLook {
	return dotLook{off: off, mono: m.mono || os.Getenv("NO_COLOR") != ""}
}

// mark is the dot drawn on base.
func (d reviewDot) mark(base lipgloss.Style, l dotLook) string {
	switch {
	case d == dotNone:
		return ""
	case l.off:
		return base.Render(" ")
	case l.mono:
		return base.Render(dotGlyphs[d])
	}
	return base.Foreground(lipgloss.Color(dotColours[d])).Render("●")
}

// blinks: the dot is a process.
func (d reviewDot) blinks() bool { return d == dotGreen || d == dotYellow }

// worst is the more urgent of two dots.
func worst(a, b reviewDot) reviewDot { return max(a, b) }

// askedOpen are a request's states while it is under way there.
var askedOpen = map[string]bool{"asked": true, "waiting": true, "requested": true, "accepted": true, "submitted": true, "publishing": true}

// askedFailed are a request's failed states, settled or not.
var askedFailed = map[string]bool{"publication_failed": true, "uncertain": true, "refused": true, "unsent": true,
	"expired": true, "review_expired": true, "contact_removed": true}

// askDot is a request's dot, by its state.
func askDot(state string) reviewDot {
	switch {
	case askedFailed[state]:
		return dotRed
	case askedOpen[state]:
		return dotGreen
	}
	return dotNone
}

// reviewHereDot is a review's dot: under way, or its publication failed.
func reviewHereDot(r protocol.ReviewView) reviewDot {
	switch {
	case openReview(r.State):
		return dotGreen
	case r.Publication.State == protocol.PublicationFailed || r.Publication.State == protocol.PublicationUncertain:
		return dotRed
	}
	return dotNone
}

// rowMark is a panel row's dot and a space, blinking with the footer's.
func (m *Model) rowMark(d reviewDot) string {
	if d == dotNone {
		return "  "
	}
	return d.mark(lipgloss.NewStyle(), m.look(d.blinks() && m.reviewSig.dotOff)) + " "
}

// theirState names a request's state as the other workspace's, escaped:
// who took it there, or what happened there.
func theirState(state, reviewer string, ws workspaceLabel) string {
	switch state {
	case "accepted":
		return "accepted by " + safe(reviewer) + "@" + string(ws)
	case "requested", "waiting", "submitted", "publishing", "cancelled", "declined", "withheld":
		return state + " there"
	case "review_expired":
		return "expired there"
	}
	return safe(state)
}

// footerCounts are the running reviews and open requests, as words.
func footerCounts(v ReviewSignalView) []string {
	var words []string
	if v.Process == "" && v.AwaitingYou > 0 {
		words = append(words, fmt.Sprintf("%d need you", v.AwaitingYou)) // stale counts: said, not signalled
	}
	if v.Running > 0 {
		words = append(words, fmt.Sprintf("%d running", v.Running))
	}
	if v.AskedOpen > 0 {
		words = append(words, fmt.Sprintf("%d asked", v.AskedOpen))
	}
	return words
}

// footerParts are the footer's pieces: a failure first, then the process
// with its dot leading, then where to look.
func (m *Model) footerParts(v ReviewSignalView) []string {
	var parts []string
	switch {
	case v.RedShown:
		parts = append(parts, dotRed.mark(bandStyle, m.look(false))+bandStyle.Render(fmt.Sprintf(" %d failed", v.Failed)))
	case v.Stale && v.Failed > 0:
		parts = append(parts, bandStyle.Render(fmt.Sprintf("%d failed", v.Failed)))
	}
	counts := footerCounts(v)
	process := processDot(v)
	dot := process.mark(bandStyle, m.look(!v.DotShown))
	switch process {
	case dotYellow:
		parts = append(parts, dot+bandWarn.Render(fmt.Sprintf(" %d need you", v.AwaitingYou)), bandWarn.Render("/reviews"))
		return append(parts, bandWords(counts)...)
	case dotGreen:
		parts = append(parts, dot+bandStyle.Render(" "+counts[0]))
		counts = counts[1:]
	}
	parts = append(parts, bandWords(counts)...)
	return append(parts, bandStyle.Render("/reviews"))
}

func bandWords(words []string) []string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = bandStyle.Render(w)
	}
	return out
}

// processDot is the footer's process, by its name in the view.
func processDot(v ReviewSignalView) reviewDot {
	return map[string]reviewDot{"yellow": dotYellow, "green": dotGreen}[v.Process]
}
