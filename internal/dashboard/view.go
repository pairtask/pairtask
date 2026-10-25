package dashboard

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m *model) View() tea.View {
	lines := m.header()
	bodyHeight := max(1, m.height-len(lines)-3)
	lines = append(lines, m.body(viewport{m.width, bodyHeight})...)
	lines = append(lines, "", m.styles.muted.Render((viewport{width: m.width}).controls()),
		m.styles.muted.Render("Read-only · Connected means transport, not model activity."))
	if m.width < 35 || m.height < 12 {
		lines = []string{"collab-ai · " + safe(m.latest.Health), "Resize to at least 35 x 12", "q quit · collab status for text"}
	}
	v := tea.NewView(strings.Join((viewport{m.width, m.height}).fit(lines), "\n"))
	v.AltScreen = true
	return v
}

func (m *model) header() []string {
	busy := ""
	if m.inFlight {
		busy = " · refreshing…"
	}
	metrics := "No snapshot yet"
	if m.haveSnapshot {
		metrics = fmt.Sprintf("Connected %s · Pending deliveries %s · Operator inbox %s · %s", number(m.snapshot.ConnectedSessions), pendingTotal(m.snapshot.DurablePending), operatorInbox(m.snapshot.OperatorPending), m.claimsMetric())
	}
	return []string{
		m.styles.title.Render("collab-ai  /  operator console"),
		m.styles.muted.Render("Socket " + safe(m.cfg.Socket)),
		m.styles.accent.Render(fmt.Sprintf("Broker %s · Reachable %t%s", safe(m.latest.Health), m.latest.Reachable, busy)),
		m.freshness(),
		metrics,
		m.notices(),
	}
}

func (m *model) freshness() string {
	if !m.haveSnapshot {
		return "Waiting for a broker snapshot"
	}
	tag := "OBSERVED"
	if m.snapshot.Health == "degraded" {
		tag = "DEGRADED"
	}
	if m.stale(time.Now()) {
		tag = "STALE — retained observation"
	}
	age := max(time.Duration(0), time.Since(*m.snapshot.SnapshotAt)).Round(time.Second)
	return fmt.Sprintf("%s · %s (%s ago)", tag, stamp(m.snapshot.SnapshotAt), age)
}

func (m *model) notices() string {
	notes := append(m.snapshotNotes(), m.agentStatusNotes()...)
	if m.latest.Error != "" {
		notes = append(notes, "Error: "+safe(m.latest.Error))
	}
	if len(notes) == 0 {
		return m.styles.muted.Render("Snapshots only · refresh " + m.cfg.Interval.String() + " after each response")
	}
	return strings.Join(notes, " · ")
}

// snapshotNotes are the session and inbox gaps of the snapshot.
func (m *model) snapshotNotes() []string {
	var notes []string
	if m.haveSnapshot && !m.snapshot.HistoryAvailable {
		notes = append(notes, "history unavailable")
	}
	if m.snapshot.SessionsTruncated {
		notes = append(notes, "session list truncated")
	}
	if p := m.snapshot.DurablePending; p != nil && p.RecipientsTruncated {
		notes = append(notes, "inbox list truncated")
	}
	return notes
}

// agentStatusNotes are the gaps in the agents' declarations.
func (m *model) agentStatusNotes() []string {
	switch {
	case m.snapshot.AgentStatusTruncated:
		return []string{"agent status truncated"}
	case m.haveSnapshot && !m.snapshot.AgentStatusAvailable:
		return []string{"agent status unavailable"}
	}
	return nil
}

func (m *model) body(area viewport) []string {
	if m.help {
		return m.scrolled(helpText, area)
	}
	if m.detail {
		return m.scrolled(m.details(), area)
	}
	if m.width >= 105 {
		leftWidth := m.width / 2
		left := m.roster(viewport{leftWidth, area.height})
		right := m.scrolled(m.details(), viewport{area.width - leftWidth - 3, area.height})
		lines := make([]string, area.height)
		for i := range lines {
			lines[i] = pad(at(left, i), leftWidth) + " │ " + at(right, i)
		}
		return lines
	}
	return m.roster(area)
}

func (m *model) roster(area viewport) []string {
	height, width := area.height, area.width
	title := "Sessions · state " + stateLabel(states[m.state])
	if m.tab == 1 {
		title = "Pending inboxes · one row per logical ID"
	}
	filter := safe(m.filter)
	if m.editing {
		filter += "_"
	}
	lines := []string{m.styles.accent.Render(title), "Filter: " + filter}
	rows := m.rows()
	if len(rows) == 0 {
		return area.fit(append(lines, m.emptyMessage()))
	}
	count := max(1, height-3)
	start := max(0, m.cursor-count+1)
	for i := start; i < min(len(rows), start+count); i++ {
		line := "  " + m.rowText(rows[i], width-2)
		if i == m.cursor {
			line = m.styles.selected.Render("> " + m.rowText(rows[i], width-2))
		}
		lines = append(lines, line)
	}
	lines = append(lines, m.styles.muted.Render(fmt.Sprintf("%d/%d shown · enter opens full details", m.cursor+1, len(rows))))
	return area.fit(lines)
}

func (m *model) rowText(r row, width int) string {
	if r.session == nil {
		return pad(safe(r.id.agent), max(1, width-10)) + fmt.Sprintf("  %d", r.pending)
	}
	state := stateLabel(r.session.State)
	agentWidth := max(1, width-16)
	if width >= 90 {
		agentWidth = width - 63
		return pad(safe(r.id.agent), agentWidth) + "  " + pad(state, 13) + "  " + ansi.Truncate(safe(r.id.session), 18, "…") +
			"  " + ansi.Truncate(m.statusCell(r.session), 24, "…")
	}
	if width >= 60 {
		agentWidth = width - 37
		return pad(safe(r.id.agent), agentWidth) + "  " + pad(state, 13) + "  " + ansi.Truncate(safe(r.id.session), 18, "…")
	}
	return pad(safe(r.id.agent), agentWidth) + "  " + state
}

func (m *model) emptyMessage() string {
	if !m.haveSnapshot {
		return "No observations available. Retrying automatically."
	}
	if m.tab == 1 && m.snapshot.DurablePending == nil {
		return "Pending counts unavailable."
	}
	return "No matching rows in this snapshot."
}

func (m *model) scrolled(text string, area viewport) []string {
	lines := strings.Split(ansi.Wrap(text, area.width, ""), "\n")
	offset := min(m.scroll, max(0, len(lines)-area.height))
	return area.fit(lines[offset:])
}

func (m *model) scrollLimit() int {
	text := m.details()
	if m.help {
		text = helpText
	}
	lines := strings.Split(ansi.Wrap(text, m.width, ""), "\n")
	return max(0, len(lines)-max(1, m.height-9))
}

func (area viewport) controls() string {
	width := area.width
	if width < 45 {
		return "? help · q quit"
	}
	if width < 65 {
		return "↑↓ move  enter details  ? help  q quit"
	}
	if width < 100 {
		return "↑↓ move  enter details  tab view  / filter  ? help  q quit"
	}
	return "↑↓ move  enter details  tab inboxes  / filter  s state  r refresh  ? help  q quit"
}
