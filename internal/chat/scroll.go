package chat

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// viewport is the transcript's scroll position. While following, the newest
// line is at the bottom. Otherwise the view is anchored to an entry and a line
// within it, so arrivals below and history above never move what you read.
type viewport struct {
	following bool
	anchorID  string
	anchorSrc int // logical line within the entry
	anchorCol int // cell offset within that logical line
}

// top is the index of the first visible line for a transcript of lines and a
// window of height rows.
func (m *Model) top(lines []renderLine, height int) int {
	bottom := max(0, len(lines)-height)
	if m.view.following {
		return bottom
	}
	return max(0, min(m.anchorIndex(lines), bottom))
}

// anchorIndex finds the wrapped segment that holds the anchored text: the last
// segment of the anchored logical line starting at or before the anchored
// offset. A vanished entry falls back to the bottom.
func (m *Model) anchorIndex(lines []renderLine) int {
	found := -1
	for i, l := range lines {
		if l.id != m.view.anchorID {
			continue
		}
		if found < 0 {
			found = i
		}
		if m.view.reaches(l) {
			found = i
		}
	}
	if found < 0 {
		return len(lines)
	}
	return found
}

// transcriptWindow is the visible part of the transcript.
func (m *Model) transcriptWindow(height int) []string {
	lines := m.transcript()
	start := m.top(lines, height)
	end := min(len(lines), start+height)
	window := make([]string, 0, end-start)
	for _, l := range lines[start:end] {
		window = append(window, l.text)
	}
	return window
}

// scrollPage moves a page up (-1) or down (1). Paging up past the oldest
// loaded message loads older history.
func (m *Model) scrollPage(direction int) tea.Cmd {
	if m.panel != nil {
		return nil
	}
	height := max(1, m.layout().transcript)
	lines := m.transcript()
	start := m.top(lines, height)
	bottom := max(0, len(lines)-height)
	if direction < 0 && start == 0 {
		m.anchor(lines, 0)
		m.startOlderRun()
		return m.loadOlder(nil)
	}
	next := start + direction*max(1, height-1)
	if next >= bottom {
		m.follow()
		return nil
	}
	m.anchor(lines, max(0, next))
	return nil
}

// startOlderRun begins paging global history for a filtered view.
func (m *Model) startOlderRun() {
	if !m.conv.selected.common() && m.history.run == nil {
		m.history.run = &olderRun{key: m.conv.selected}
	}
}

// anchor pins the view to the line at index.
func (m *Model) anchor(lines []renderLine, index int) {
	if len(lines) == 0 {
		return
	}
	l := lines[min(index, len(lines)-1)]
	m.view = viewport{anchorID: l.id, anchorSrc: l.src, anchorCol: l.col}
}

// follow returns to the newest message; what it shows is no longer unread.
func (m *Model) follow() {
	m.view = viewport{following: true}
	m.markShown()
}

// scrollOffset is how many transcript lines are below the visible window.
func (m *Model) scrollOffset() int {
	if m.view.following {
		return 0
	}
	height := max(1, m.layout().transcript)
	lines := m.transcript()
	return max(0, len(lines)-(m.top(lines, height)+height))
}

// findCommand finds the newest message containing text, visible rows
// included; /find alone continues with older matches from the last one.
func (m *Model) findCommand(arg argument) tea.Cmd {
	below := uint64(0) // 0: search everything
	if arg.empty() {
		below = m.found
	} else {
		m.find, m.found = string(arg), 0
	}
	if m.find == "" {
		m.status = "/find needs text"
		return nil
	}
	lines := m.transcript()
	e, at := m.findBelow(lines, below)
	if e == nil {
		m.status = "no more messages contain " + safe(m.find)
		return nil
	}
	m.anchor(lines, at)
	m.found = e.msg.Seq
	m.status = fmt.Sprintf("found #%d · /find continues", e.msg.Seq)
	return nil
}

// findBelow is the newest visible entry older than below (0: any) whose text
// contains the search, and its first line.
func (m *Model) findBelow(lines []renderLine, below uint64) (*entry, int) {
	want := strings.ToLower(m.find)
	for i := len(m.timeline.order) - 1; i >= 0; i-- {
		e := m.timeline.order[i]
		if below != 0 && e.msg.Seq >= below {
			continue
		}
		if at := firstLine(lines, e.msg.MessageID); at >= 0 && contains(e, want) {
			return e, at
		}
	}
	return nil, -1
}

// contains reports whether e's text includes want (already lower-cased).
func contains(e *entry, want string) bool {
	if e.omitted {
		return false
	}
	text := payloadText(e.msg.Payload)
	return strings.Contains(strings.ToLower(text), want) || strings.Contains(strings.ToLower(plainText(text)), want)
}

func firstLine(lines []renderLine, id string) int {
	for i, l := range lines {
		if l.id == id {
			return i
		}
	}
	return -1
}

// Panels replace the transcript until Esc.
const (
	panelHelp   = "help"
	panelAgents = "agents"
	panelStages = "stages"
)

type panel struct {
	kind   string
	seq    uint64
	id     string
	offset int    // first visible row, moved with PgUp/PgDn
	rows   int    // rows at the last render, to bound scrolling
	code   string // a pairing panel's one-time code; gone with the panel
}

// scroll moves the panel by a page. A panel taller than the window shows
// height-1 rows plus a position line, so that is the page.
func (p *panel) scroll(direction, height int) {
	page := max(1, height-1)
	p.offset = max(0, min(p.offset+direction*page, max(0, p.rows-page)))
}

// panelBuilders are each panel's rows.
var panelBuilders = map[string]func(*Model) []string{
	panelHelp: (*Model).helpRows,
	panelAgents: func(m *Model) []string {
		return append([]string{titleStyle.Render("Agents") + mutedStyle.Render("  (Esc closes)")}, m.agentRows()...)
	},
	panelStages:  (*Model).stageRows,
	panelClaims:  (*Model).claimRows,
	panelRooms:   (*Model).roomRows,
	panelPairing: (*Model).pairingRows,
}

func (m *Model) panelRows(height int) []string {
	var rows []string
	if build := panelBuilders[m.panel.kind]; build != nil {
		rows = build(m)
	}
	var out []string
	for _, row := range rows {
		out = append(out, wrap(row, max(minWidth, m.width))...)
	}
	m.panel.rows = len(out)
	if len(out) <= height {
		return out
	}
	// One row reports the position; every content row is reachable with PgDn.
	body := max(1, height-1)
	start := max(0, min(m.panel.offset, len(out)-body))
	end := min(len(out), start+body)
	rows = append([]string(nil), out[start:end]...)
	if height > 1 {
		rows = append(rows, mutedStyle.Render(fmt.Sprintf("… rows %d–%d of %d · PgUp/PgDn", start+1, end, len(out))))
	}
	return rows
}

func (m *Model) stageRows() []string {
	rows := []string{titleStyle.Render(fmt.Sprintf("Delivery of #%d", m.panel.seq)) + mutedStyle.Render("  (Esc closes)")}
	e := m.timeline.entries[m.panel.id]
	if e == nil {
		return append(rows, "that message is no longer loaded")
	}
	for _, st := range e.view().Stages {
		rows = append(rows, fmt.Sprintf("  %s  %s", safe(st.AgentID), stageLabel(st.Stage)))
	}
	if e.stagesTruncated {
		rows = append(rows, mutedStyle.Render(fmt.Sprintf("  more recipients: /stages #%d", e.msg.Seq)))
	}
	if len(rows) == 1 {
		rows = append(rows, "  no recipient stages known yet")
	}
	return rows
}

func (m *Model) helpRows() []string {
	support := "not reported by this terminal: Shift+Enter may send; use Alt+Enter, Ctrl+J or \\ then Enter"
	if m.keyboard {
		support = "reported by this terminal"
	}
	rows := []string{
		titleStyle.Render(chatTitle) + mutedStyle.Render("  (F1 or Esc closes)"),
		"",
		"Sending   no mention → everyone · @codex-1 @claude-1 text → just them · @all → everyone",
		"          mentions count only at the start; later @ is plain text · \\@ starts with a literal @",
		"          pasted text is literal (mentions ignored) · /to [\"id\"] for IDs that aren't @-able",
		"Keys      Enter send · Shift+Enter, Alt+Enter, Ctrl+J, \\ then Enter: new line",
		"          PgUp/PgDn scroll · Ctrl+End newest · Ctrl+R reply to newest · Ctrl+K commands · Ctrl+Q quit",
		"          mouse wheel scrolls (Shift, Option on macOS, selects text) · Up in an empty input recalls your last message",
		"Commands",
	}
	for _, c := range commands {
		rows = append(rows, fmt.Sprintf("  /%-8s %-14s %s", c.name, c.usage, c.help))
	}
	return append(rows, "", "Shift+Enter detection: "+support)
}

func (m *Model) toggleHelp() {
	if m.panel != nil && m.panel.kind == panelHelp {
		m.panel = nil
		return
	}
	m.panel = &panel{kind: panelHelp}
}

// agentRows describes every known agent: online first, then seen.
func (m *Model) agentRows() []string {
	rows := append(m.onlineRows(), m.statusRows()...)
	for _, id := range m.conversationIDs("") {
		rows = append(rows, "○ "+safe(id)+"  seen in this conversation")
	}
	if m.agents.partial {
		rows = append(rows, fmt.Sprintf("(presence list is partial: %d connected in total)", m.agents.total))
	}
	if len(rows) == 0 {
		rows = []string{"no agents online or seen yet"}
	}
	return rows
}

func (m *Model) onlineRows() []string {
	var rows []string
	for _, a := range m.agents.list {
		rows = append(rows, fmt.Sprintf("● %s  online · protocol v%d%s", safe(a.AgentID), a.ProtocolVersion, privateBroadcastNote(a.ProtocolVersion)))
	}
	sort.Strings(rows)
	return rows
}

// privateBroadcastNote warns that an agent below v7 still broadcasts to
// agents only: its "*" messages never reach the operator.
func privateBroadcastNote(version int) string {
	if version >= protocol.PublicBroadcastVersion {
		return ""
	}
	return " · its broadcasts don't reach you (below v7)"
}

// reaches is a row at or before the anchor: the last such row is the
// anchor's, so a slot a drawn diagram left empty resolves to the row above
// it, never the message start.
func (v viewport) reaches(l renderLine) bool {
	if l.src != v.anchorSrc {
		return l.src < v.anchorSrc
	}
	return l.col <= v.anchorCol
}
