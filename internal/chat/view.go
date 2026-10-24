package chat

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Palette: a quieter 256-colour text palette, and bands (header, footer,
// selection) that set both foreground and background so their contrast does
// not depend on the terminal theme; a selection is a dark blue band with
// light text, never reverse video over faint text.
var (
	titleStyle = lipgloss.NewStyle().Bold(true)
	mutedStyle = lipgloss.NewStyle().Faint(true)
	warnStyle  = lipgloss.NewStyle().Bold(true)
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Bold(true) // soft red
	youStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("110")).Bold(true) // steel blue
	pickStyle  = lipgloss.NewStyle().Background(lipgloss.Color("24")).Foreground(lipgloss.Color("255"))
	needStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("179")).Bold(true) // gold
	queueStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	doneStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("108")) // sage
	agentHues  = []string{"73", "108", "139", "173", "110", "179", "150", "140", "174", "109"}
	// The header and footer are slate bands; the audience divider names where
	// the draft goes, everyone in the warning colour so a broadcast is never a
	// surprise.
	bandStyle     = lipgloss.NewStyle().Background(lipgloss.Color("237")).Foreground(lipgloss.Color("252"))
	bandTitle     = bandStyle.Bold(true)
	bandWarn      = bandStyle.Foreground(lipgloss.Color("214")).Bold(true)
	everyoneStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("179")).Bold(true)
	ruleStyle     = lipgloss.NewStyle().Faint(true)
)

const (
	chatTitle   = "pairtask operator chat"
	keyReminder = "Enter send · / commands · F1 help"
	rule        = "─"
)

// Minimum terminal size; below it the chat shows a notice and won't send.
const (
	minWidth  = 20
	minHeight = 4
	pickerMax = 6
)

func agentStyle(id string) lipgloss.Style {
	h := fnv.New32a()
	h.Write([]byte(id))
	return lipgloss.NewStyle().Foreground(lipgloss.Color(agentHues[h.Sum32()%uint32(len(agentHues))])).Bold(true)
}

func (m *Model) tooSmall() bool {
	return m.width < minWidth || m.height < minHeight
}

// View renders the whole screen on the alternate screen: every view sets it,
// so frames never scroll into the terminal's own history.
func (m *Model) View() tea.View {
	m.markShown()
	view := tea.NewView(m.screen())
	view.AltScreen = true
	// Wheel events scroll the transcript; terminals then need Shift (Option
	// on macOS) held for native text selection, and /copy stays the
	// clipboard path.
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

func (m *Model) screen() string {
	if m.tooSmall() {
		return fitLines("terminal too small", m.width)
	}
	l := m.layout()
	var rows []string
	if l.header {
		rows = append(rows, m.titleBand())
	}
	if l.bar {
		rows = append(rows, m.barRow())
	}
	if l.divider {
		rows = append(rows, m.rule())
	}
	rows = append(rows, m.mainArea(l)...)
	rows = append(rows, m.audienceDivider(l.boxed))
	rows = append(rows, m.composerRows(l)...)
	if l.boxed {
		rows = append(rows, m.boxBottom())
	}
	rows = append(rows, m.pickerRows(l)...)
	rows = append(rows, m.footer())
	return fitLines(strings.Join(rows, "\n"), m.width)
}

// entryRows is what the newest message needs to show its header, the first
// line of its text and its acknowledgment mark; a longer text wraps past it.
const entryRows = 3

// frameLayout is how many rows each part gets. The audience divider, one
// composer row and the footer come first, then transcript rows for one
// message, which outrank every decoration, then the title band, the conversation bar, the
// rule under it, the box's bottom edge, more composer rows and the picker;
// the transcript takes the rest.
type frameLayout struct {
	header, bar, divider, boxed bool
	transcript                  int
	picker, composer            int
}

func (m *Model) layout() frameLayout {
	l := frameLayout{composer: 1}
	rest := m.height - 3                        // audience divider + one composer row + footer
	l.transcript = max(0, min(entryRows, rest)) // the newest message before any decoration
	rest -= l.transcript
	rest = m.layoutTop(&l, rest)
	rest = m.layoutBottom(&l, rest)
	l.transcript += max(0, rest)
	return l
}

// layoutTop allocates the title band, the conversation bar and the rule
// under the bar, in that order.
func (m *Model) layoutTop(l *frameLayout, rest int) int {
	if rest > 0 {
		l.header, rest = true, rest-1
	}
	if rest > 0 {
		l.bar, rest = true, rest-1
	}
	if rest > 0 {
		l.divider, rest = true, rest-1
	}
	return rest
}

// layoutBottom allocates the box's bottom edge, the extra composer rows and
// the picker. The box needs the width for its edges too.
func (m *Model) layoutBottom(l *frameLayout, rest int) int {
	if rest > 0 && m.width >= boxMinWidth {
		l.boxed, rest = true, rest-1
	}
	want := m.compose.editor.Height() - 1
	if m.compose.cmd != nil {
		want = 0
	}
	extra := max(0, min(want, rest-2))
	l.composer += extra
	rest -= extra
	if p := m.compose.picker; p != nil {
		l.picker = min(len(p.items), pickerMax, max(0, rest-1))
		rest -= l.picker
	}
	return rest
}

// titleBand is one colour band across the width with the title alone; the
// facts live in the footer, where the eye is while typing.
func (m *Model) titleBand() string {
	title := chatTitle
	if m.width < 52 {
		title = "operator chat"
	}
	return bandTitle.Render(title) + bandStyle.Render(strings.Repeat(" ", max(0, m.width-ansi.StringWidth(title))))
}

// footer is the colour band under the input. Its parts come in priority
// order while they fit: a status (a command's result or a refusal), the ack
// reminder with its bound warning, the connection facts, then the key
// reminder on the right unless a status replaced it.
func (m *Model) footer() string {
	status := m.statusLine()
	var parts []string
	if status != "" {
		parts = append(parts, bandTitle.Render(ansi.Truncate(status, m.width, "…")))
	}
	if reminder := m.ackReminder(); reminder != "" {
		parts = append(parts, reminder)
	}
	left, used := fitParts(append(parts, bandStyle.Render(m.facts())), m.width)
	right := ""
	if gap := m.width - used - ansi.StringWidth(keyReminder); gap >= 2 && status == "" {
		right = bandStyle.Render(strings.Repeat(" ", gap) + keyReminder)
		used = m.width
	}
	return left + right + bandStyle.Render(strings.Repeat(" ", max(0, m.width-used)))
}

// fitParts joins the parts that fit in the width with " · ", in order; the
// first part is cut to the width rather than dropped. Returns the band and
// the cells it uses.
func fitParts(parts []string, width int) (string, int) {
	line, used := "", 0
	for _, part := range parts {
		need := ansi.StringWidth(part)
		if line == "" && need > width {
			part, need = ansi.Truncate(part, width, "…"), width
		}
		if line != "" {
			need += 3
		}
		if used+need > width {
			continue
		}
		if line != "" {
			line += bandStyle.Render(" · ")
		}
		line, used = line+part, used+need
	}
	return line, used
}

// facts is the connection state and the counts, shorter on narrow terminals.
func (m *Model) facts() string {
	conn := m.conn
	if m.conn == Connected && !m.baseline {
		conn = "syncing"
	}
	online := len(m.agents.list)
	switch {
	case m.width >= 76:
		return fmt.Sprintf("%s · %d online · unread %d · inbox %d", conn, online, m.unreadIn(allKey), m.inbox.pending)
	case m.width >= 52:
		return fmt.Sprintf("%s · %d online · %d unread", conn, online, m.unreadIn(allKey))
	}
	return fmt.Sprintf("%s · %d on · %d new", conn, online, m.unreadIn(allKey))
}

// rule is the divider between the header and the transcript.
func (m *Model) rule() string {
	return ruleStyle.Render(strings.Repeat(rule, max(0, m.width)))
}

// mainArea is the transcript, or an open panel in its place.
func (m *Model) mainArea(l frameLayout) []string {
	if l.transcript == 0 {
		return nil
	}
	lines := m.transcriptWindow(l.transcript)
	if m.panel != nil {
		lines = m.panelRows(l.transcript)
	}
	for len(lines) < l.transcript {
		lines = append([]string{""}, lines...)
	}
	return lines
}

func (m *Model) pickerRows(l frameLayout) []string {
	p, n := m.compose.picker, l.picker
	if p == nil || n == 0 {
		return nil
	}
	start := max(0, min(p.selected-n+1, len(p.items)-n))
	var rows []string
	for i := start; i < min(start+n, len(p.items)); i++ {
		if i == p.selected {
			// The selected row is a band: plain text, explicit colours, no
			// faint help that a reverse-video highlight would wash out.
			label := ansi.Strip(m.pickerLabel(p, p.items[i]))
			rows = append(rows, pickStyle.Render(label+strings.Repeat(" ", max(0, m.width-ansi.StringWidth(label)))))
			continue
		}
		rows = append(rows, m.pickerLabel(p, p.items[i]))
	}
	return rows
}

func (m *Model) pickerLabel(p *picker, item string) string {
	switch p.kind {
	case PickerAgent:
		return "  @" + safe(item)
	case PickerAck:
		return m.ackLabel(item)
	}
	c := byName(item)
	return fmt.Sprintf("  /%s %s  %s", c.name, c.usage, mutedStyle.Render(c.help))
}

// composerRows renders the editor at the rows the layout gave it; on a small
// terminal that is fewer than it sized itself to. Inside the box each row
// gets the side edges and a cell of padding.
func (m *Model) composerRows(l frameLayout) []string {
	m.compose.setWidth(m.editorWidth(l.boxed))
	rows := m.inputRows(l.composer)
	if !l.boxed {
		return rows
	}
	for i, row := range rows {
		rows[i] = m.boxRow(row)
	}
	return rows
}

// inputRows is the command line, or the editor at the given height.
func (m *Model) inputRows(height int) []string {
	c := &m.compose
	if c.cmd != nil {
		return []string{m.commandLineRow()}
	}
	if c.editor.Height() != height {
		c.editor.SetHeight(height)
	}
	return strings.Split(c.editor.View(), "\n")
}

// commandLineRow renders the command line at the composer's width less the
// "/" prompt and the cursor cell, so a long command scrolls inside.
func (m *Model) commandLineRow() string {
	in := &m.compose.cmd.input
	if want := m.compose.width - 2; in.Width() != want {
		in.SetWidth(want)
		// The input re-places its window only when the cursor leaves it, so
		// a stale window survives a narrower width: anchor it at the end,
		// which always recomputes, then let the cursor pull it back.
		pos := in.Position()
		in.CursorEnd()
		in.SetCursor(pos)
	}
	return in.View()
}

// boxMinWidth is the narrowest terminal that gets the input box; below it
// the edges would cost too much of the line.
const boxMinWidth = 24

// editorWidth is the outer width the editor renders at: the window, less the
// box edges and padding when boxed.
func (m *Model) editorWidth(boxed bool) int {
	if boxed {
		return max(10, m.width-4)
	}
	return max(10, m.width)
}

// boxRow is one composer row between the box's side edges.
func (m *Model) boxRow(row string) string {
	inner := m.width - 4
	row = ansi.Truncate(row, inner, "")
	pad := strings.Repeat(" ", max(0, inner-ansi.StringWidth(row)))
	return ruleStyle.Render("│ ") + row + pad + ruleStyle.Render(" │")
}

// boxBottom closes the input box.
func (m *Model) boxBottom() string {
	return ruleStyle.Render("╰" + strings.Repeat(rule, max(0, m.width-2)) + "╯")
}

func (m *Model) statusLine() string {
	var parts []string
	if in := m.send.intent; in != nil && in.State != IntentAccepted {
		parts = append(parts, intentLine(in))
	}
	if m.timeline.paused {
		parts = append(parts, "capacity reached: receiving paused")
	}
	if m.status != "" {
		parts = append(parts, safe(m.status))
	}
	return strings.Join(parts, " · ")
}

func intentLine(in *intent) string {
	line := fmt.Sprintf("send %s: %s", shortID(in.ID), in.State)
	if len(in.Excluded) > 0 {
		quoted := make([]string, len(in.Excluded))
		for i, id := range in.Excluded {
			quoted[i] = strconv.Quote(id)
		}
		line += " (excluded: " + strings.Join(quoted, ", ") + ")"
	}
	if in.Detail != "" {
		line += " " + safe(in.Detail)
	}
	return line
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// hintText is the audience line as plain text: who the draft goes to, or why
// it can't be sent.
func (m *Model) hintText() string {
	if m.compose.cmd != nil {
		return "command · Enter runs · Tab completes · Esc back to your draft"
	}
	a := m.resolveAudience()
	if a.err != "" {
		return "✗ " + a.err
	}
	text := m.audienceText(a)
	if len(a.notSeen) > 0 {
		text += " (not seen: " + safe(strings.Join(a.notSeen, ", ")) + ")"
	}
	if m.compose.pasted {
		text += " · pasted text, mentions ignored"
	}
	return text
}

func (m *Model) audienceText(a audience) string {
	switch {
	case a.source == SourceReply:
		r := m.compose.reply
		return fmt.Sprintf("reply to #%d → %s", r.seq, safe(r.sender))
	case a.everyone:
		return fmt.Sprintf("to everyone (%d online%s)", len(m.agents.list), m.partialNote())
	}
	ids := make([]string, len(a.ids))
	for i, id := range a.ids {
		ids[i] = safe(id)
	}
	return "to " + strings.Join(ids, ", ")
}

func (m *Model) partialNote() string {
	if m.agents.partial {
		return ", partial list"
	}
	return ""
}

// audienceDivider is the rule above the composer that names where the draft
// goes: "── to codex-1 ────" in that agent's colour, everyone in the warning
// colour, a blocking reason in the error colour. Boxed, it is the top edge:
// "╭─ to codex-1 ────╮".
func (m *Model) audienceDivider(boxed bool) string {
	text := m.hintText()
	if boxed {
		text = ansi.Truncate(text, max(0, m.width-6), "…")
	}
	label := m.audienceStyle(text).Render(text)
	if !boxed {
		fill := max(0, m.width-ansi.StringWidth(text)-4)
		return ruleStyle.Render(rule+rule+" ") + label + ruleStyle.Render(" "+strings.Repeat(rule, fill))
	}
	fill := max(0, m.width-ansi.StringWidth(text)-5)
	return ruleStyle.Render("╭"+rule+" ") + label + ruleStyle.Render(" "+strings.Repeat(rule, fill)+"╮")
}

// audienceStyle colours the audience label by what the draft would do.
func (m *Model) audienceStyle(text string) lipgloss.Style {
	if strings.HasPrefix(text, "✗") {
		return errStyle
	}
	if m.compose.cmd != nil {
		return titleStyle
	}
	a := m.resolveAudience()
	switch {
	case a.source == SourceReply:
		return agentStyle(m.compose.reply.sender)
	case a.everyone:
		return everyoneStyle
	case len(a.ids) > 0:
		return agentStyle(a.ids[0])
	}
	return titleStyle
}
