package chat

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/pairtask/pairtask/internal/protocol"
)

// renderLine is one visual line of the transcript: the entry it belongs to,
// the logical line within the entry (src) and the cell offset of this wrapped
// segment within that logical line (col). Anchoring by src and col keeps the
// same text in view when a resize re-wraps it.
type renderLine struct {
	id       string
	src, col int
	text     string
}

// indent is the prefix of an entry's wrapped lines.
type indent string

const (
	flush  indent = ""
	body   indent = "  "
	bullet indent = "  · "
)

// transcriptWidth is the width entries are laid out at.
func (m *Model) transcriptWidth() int {
	return max(minWidth, m.width)
}

// transcript lays out every visible entry at the current width.
func (m *Model) transcript() []renderLine {
	var lines []renderLine
	for _, e := range m.timeline.order {
		if e.msg.Seq <= m.cutoff || !m.inView(e, m.conv.selected) {
			continue
		}
		lines = append(lines, m.entryLines(e)...)
	}
	return lines
}

// entryLines lays out one entry as logical lines, each wrapped to the width.
func (m *Model) entryLines(e *entry) []renderLine {
	width := m.transcriptWidth()
	b := lineBuilder{id: e.msg.MessageID, seq: e.msg.Seq, renders: e.drawingsFor()}
	b.add([]string{""}, flush)
	b.add(e.headerAt(width, m.entryHeader), flush)
	if e.msg.InReplyTo != "" {
		b.add([]string{mutedStyle.Render("↳ " + m.replyLabel(e))}, body)
	}
	if e.omitted {
		b.add([]string{mutedStyle.Render("[text not available: this message exceeds the history page size]")}, body)
	} else {
		b.addLaidOut(e.bodyAt(width))
	}
	b.add(m.claimLines(e, width-len(body)), body)
	b.add(e.statusAt(m.statusText(e), width), body)
	if e.gapBelow {
		b.add([]string{warnStyle.Render("── older messages may be missing here · /history loads them ──")}, body)
	}
	return b.lines
}

// lineBuilder numbers logical lines and the offsets of their wrapped parts.
type lineBuilder struct {
	id      string
	seq     uint64          // the message number diagram labels refer to; 0 for none
	renders *diagramRenders // nil draws without a cache
	inert   bool            // diagrams stay plain code: no label, no drawing
	src     int
	lines   []renderLine
}

// bodyLayout is an entry's body laid out at one width, with logical line
// numbers starting at 0. Formatting the body is the costly part of a redraw
// and depends only on the body (its hash), the width and the entry's number,
// so it is reused until either changes.
type bodyLayout struct {
	hash  string
	width int
	lines []renderLine
	srcs  int // logical lines used
}

// bodyAt is e's body laid out at width, formatted at most once per hash and
// width.
func (e *entry) bodyAt(width int) *bodyLayout {
	if e.body.fits(e.hash, width) {
		return e.body
	}
	b := lineBuilder{id: e.msg.MessageID, seq: e.msg.Seq, renders: e.drawingsFor()}
	b.formatBody(payloadText(e.msg.Payload), width)
	e.body = &bodyLayout{hash: e.hash, width: width, lines: b.lines, srcs: b.src}
	return e.body
}

// headerLayout is an entry's header at one width. The header reads only the
// message's own fields, which don't change, so the width is the whole key.
type headerLayout struct {
	width int
	parts []string
}

func (e *entry) headerAt(width int, render func(*entry) []string) []string {
	if h := e.header; h != nil && h.width == width {
		return h.parts
	}
	e.header = &headerLayout{width: width, parts: render(e)}
	return e.header.parts
}

// statusLayout is an entry's delivery-state lines for one state text and
// width; the text is cheap to build, wrapping and styling it is not.
type statusLayout struct {
	text  string
	width int
	parts []string
}

// fits reports whether this layout is of the given state text and width.
func (s *statusLayout) fits(text string, width int) bool {
	return s != nil && s.text == text && s.width == width
}

func (e *entry) statusAt(text string, width int) []string {
	if e.status.fits(text, width) {
		return e.status.parts
	}
	e.status = &statusLayout{text: text, width: width, parts: statusLines(e, text, width)}
	return e.status.parts
}

// fits reports whether this layout is of the given body at the given width.
func (l *bodyLayout) fits(hash string, width int) bool {
	return l != nil && l.hash == hash && l.width == width
}

// addLaidOut appends a laid-out body after the lines already built.
func (b *lineBuilder) addLaidOut(l *bodyLayout) {
	for _, line := range l.lines {
		line.src += b.src
		b.lines = append(b.lines, line)
	}
	b.src += l.srcs
}

func (b *lineBuilder) add(parts []string, in indent) {
	col := 0
	for _, part := range parts {
		b.lines = append(b.lines, renderLine{id: b.id, src: b.src, col: col, text: string(in) + part})
		col += ansi.StringWidth(ansi.Strip(part))
	}
	b.src++
}

// addText adds a wrapped plain-text line; col is each part's byte offset in
// the unwrapped text, which doesn't depend on where the wrap broke it.
func (b *lineBuilder) addText(text string, parts []string, in indent) {
	pos := 0
	for _, part := range parts {
		if i := strings.Index(text[pos:], part); i >= 0 {
			pos += i
		}
		b.lines = append(b.lines, renderLine{id: b.id, src: b.src, col: pos, text: string(in) + part})
		pos += len(part)
	}
	b.src++
}

// statusText is the entry's delivery state on one line: a received message
// leads with its acknowledgment mark, a sent one with a bullet.
func (m *Model) statusText(e *entry) string {
	if e.direction == "received" {
		return m.receivedStatus(e)
	}
	return "· " + m.sentStatus(e)
}

// statusLines wraps the delivery state; a sent message's is muted.
func statusLines(e *entry, text string, width int) []string {
	if e.direction == "received" {
		return wrap(text, width-len(body))
	}
	return styled(wrap(text, width-len(body)), mutedStyle)
}

func styled(parts []string, style lipgloss.Style) []string {
	for i, p := range parts {
		parts[i] = style.Render(p)
	}
	return parts
}

func (m *Model) entryHeader(e *entry) []string {
	width := m.transcriptWidth()
	stamp := ""
	if e.msg.TS != nil {
		stamp = e.msg.TS.Local().Format("15:04") + "  "
	}
	var who string
	if e.direction == "sent" {
		who = youStyle.Render("you") + " → " + recipientsLabel(e.msg) + mutedStyle.Render(addressedNote(e.msg.Mentions))
	} else {
		who = agentStyle(e.msg.From).Render(safe(e.msg.From)) + " → " + receivedAudience(e.msg) +
			mutedStyle.Render(addressedNote(e.msg.Mentions)+" ["+protocol.DisplayOrigin(e.msg.Origin)+"]")
	}
	left := mutedStyle.Render(stamp) + who
	seq := mutedStyle.Render(fmt.Sprintf("#%d", e.msg.Seq))
	gap := width - ansi.StringWidth(left) - ansi.StringWidth(seq)
	if gap >= 2 {
		return []string{left + strings.Repeat(" ", gap) + seq}
	}
	// Narrow: the number leads, and a long sender wraps instead of being cut.
	return wrap(seq+"  "+left, width)
}

// receivedAudience names who a received message went to: everyone for an
// agent's public "*" (v7), otherwise the operator.
func receivedAudience(msg protocol.Message) string {
	if msg.To == protocol.Broadcast {
		return roomAudience(msg)
	}
	return youStyle.Render("you")
}

func recipientsLabel(msg protocol.Message) string {
	switch {
	case msg.To == protocol.Broadcast:
		return roomAudience(msg)
	case len(msg.Group) > 0:
		ids := make([]string, len(msg.Group))
		for i, id := range msg.Group {
			ids[i] = agentStyle(id).Render(safe(id))
		}
		return strings.Join(ids, ", ")
	}
	return agentStyle(msg.To).Render(safe(msg.To))
}

// replyLabel names the message e replies to by number when it is loaded; the
// ID is a claim by the sender either way.
func (m *Model) replyLabel(e *entry) string {
	id := e.msg.InReplyTo
	if parent := m.timeline.entries[id]; parent != nil {
		return fmt.Sprintf("re #%d", parent.msg.Seq)
	}
	return "re " + strconv.Quote(safe(id)) + " (claim)"
}

// Acknowledgment states of a received message, as shown on its row.
const (
	AckNeeded   = "needs ack"
	AckQueued   = "ack queued"
	AckDone     = "acknowledged"
	ackMarkWarn = "○ "
	ackMarkWait = "◐ "
	ackMarkDone = "● "
)

// ackState is the one acknowledgment state a received message is in. An
// acknowledgment counts as queued from the operator's /ack until the broker
// confirms it, whether it waits for the receipt or is already on the wire.
func (m *Model) ackState(e *entry) string {
	id := e.msg.MessageID
	switch {
	case e.operatorStage == protocol.StageAgentAcknowledged:
		return AckDone
	case m.inbox.queuedAcks[id] != nil, m.requested(kindAck, id):
		return AckQueued
	}
	return AckNeeded
}

// ackMark styles the state as one distinct mark per message.
func (m *Model) ackMark(e *entry) string {
	switch m.ackState(e) {
	case AckDone:
		return doneStyle.Render(ackMarkDone + AckDone)
	case AckQueued:
		return queueStyle.Render(ackMarkWait + AckQueued + m.inbox.queuedAcks[e.msg.MessageID].note())
	}
	return needStyle.Render(ackMarkWarn + AckNeeded)
}

// receivedStatus is the mark, then anything that needs attention: a content
// conflict or a receipt the broker hasn't confirmed after a failure. The
// happy path (receipt pending or confirmed) shows nothing more.
func (m *Model) receivedStatus(e *entry) string {
	var notes []string
	if e.conflict {
		notes = append(notes, "CONFLICT: content changed under this ID")
	}
	if e.receipt == ReceiptFailed {
		notes = append(notes, "receipt failed, retrying")
	}
	if len(notes) == 0 {
		return m.ackMark(e)
	}
	return m.ackMark(e) + mutedStyle.Render(" · "+strings.Join(notes, " · "))
}

func (m *Model) sentStatus(e *entry) string {
	var parts []string
	if e.conflict {
		parts = append(parts, "CONFLICT: content changed under this ID")
	}
	for _, st := range e.view().Stages {
		parts = append(parts, safe(st.AgentID)+": "+stageLabel(st.Stage))
	}
	if e.stagesTruncated {
		parts = append(parts, fmt.Sprintf("more: /stages #%d", e.msg.Seq))
	}
	if len(parts) == 0 {
		return "sent"
	}
	return strings.Join(parts, " · ")
}

// note says why a queued ack is suspended, if it is; an ack on the wire has
// no queue entry and no note.
func (q *queuedAck) note() string {
	if q == nil || q.suspended == "" {
		return ""
	}
	return " (suspended: " + q.suspended + ")"
}

// roomAudience names a "*" message's audience: a room, or everyone.
func roomAudience(msg protocol.Message) string {
	if msg.Room != "" {
		return "#" + safe(msg.Room)
	}
	return "everyone"
}
