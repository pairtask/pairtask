package chat

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Agent status is what each agent declared it is doing (protocol v6), shown
// as a row in an agent's conversation and in /agents. It is never a message:
// no transcript entry, no unread count, no acknowledgment.

// kindStatusRefresh is a snapshot fetched only to update the statuses.
const kindStatusRefresh = "status_refresh"

func init() {
	responseHandlers[kindStatusRefresh] = (*Model).statusRefreshed
}

// statusView holds the latest statuses by agent and the refresh in flight.
type statusView struct {
	byAgent   map[string]protocol.AgentStatus
	total     int
	truncated bool
	inFlight  bool // a status refresh is pending
	dirty     bool // another status_changed arrived meanwhile
	ticking   bool // an age refresh is scheduled
}

// statusTick re-renders the ages; no network traffic.
type statusTick struct{}

// statusTickEvery is how often ages are re-rendered while statuses exist.
const statusTickEvery = 30 * time.Second

// ticker schedules the next age refresh while there is a status to age.
func (m *Model) statusTicker() tea.Cmd {
	if m.statuses.ticking || len(m.statuses.byAgent) == 0 {
		return nil
	}
	m.statuses.ticking = true
	return tea.Tick(statusTickEvery, func(time.Time) tea.Msg { return statusTick{} })
}

func (m *Model) onStatusTick() tea.Cmd {
	m.statuses.ticking = false
	return m.statusTicker()
}

// presence keeps a record's connected flag in step with presence events: a
// record is current only while its declaring session is the agent's session.
func (s *statusView) presence(ev *protocol.SessionEvent) {
	st, ok := s.byAgent[ev.AgentID]
	if !ok {
		return
	}
	st.Connected = ev.State == "connected" && ev.SessionID == st.SessionID
	s.byAgent[ev.AgentID] = st
}

func (s *statusView) set(snap *protocol.OperatorSnapshot) {
	s.byAgent = make(map[string]protocol.AgentStatus, len(snap.AgentStatus))
	for _, st := range snap.AgentStatus {
		s.byAgent[st.AgentID] = st
	}
	s.total, s.truncated = snap.AgentStatusTotal, snap.AgentStatusTruncated
}

// refreshStatuses fetches a snapshot for the statuses only; one at a time,
// coalescing the events that arrive meanwhile.
func (m *Model) refreshStatuses() tea.Cmd {
	if m.statuses.inFlight {
		m.statuses.dirty = true
		return nil
	}
	m.statuses.inFlight = true
	return m.request(kindStatusRefresh, protocol.Message{Type: protocol.TypeOperatorSnapshot})
}

// statusRefreshed applies the statuses when the snapshot is at the chat's own
// watermark. A snapshot past it may have cut through other events, so it is
// installed as a full baseline instead.
func (m *Model) statusRefreshed(_ pendingRequest, resp protocol.Message) tea.Cmd {
	m.statuses.inFlight = false
	if !validSnapshot(resp) {
		return m.malformed("status snapshot: " + brokerReason(resp))
	}
	wm := resp.Snapshot.Watermark
	if wm.Epoch != m.epoch || wm.Revision != m.revision {
		m.statuses.dirty = false
		return m.snapshotLoaded(resp)
	}
	m.statuses.set(resp.Snapshot)
	if m.statuses.dirty {
		m.statuses.dirty = false
		return tea.Batch(m.refreshStatuses(), m.statusTicker())
	}
	return m.statusTicker()
}

// selectedStatus is the status of the agent whose conversation is open.
func (m *Model) selectedStatus() (protocol.AgentStatus, bool) {
	if m.conv.selected.common() {
		return protocol.AgentStatus{}, false
	}
	st, ok := m.statuses.byAgent[m.conv.selected.agent()]
	return st, ok
}

// statusRow is the one-line status under the bar in an agent's conversation.
func (m *Model) statusRow() string {
	st, _ := m.selectedStatus()
	parts := []string{safe(st.AgentID) + " ▸ " + st.State}
	for _, p := range []string{st.Task, st.Step} {
		if p != "" {
			parts = append(parts, safe(p))
		}
	}
	if st.WaitingOn != "" {
		parts = append(parts, "waiting on "+st.WaitingOn)
	}
	parts = append(parts, "declared "+age(st.DeclaredAt)+" ago", activityText(st))
	if !st.Connected {
		parts = append(parts, "previous session")
	}
	return agentStyle(st.AgentID).Render(parts[0]) + mutedStyle.Render(" · "+strings.Join(parts[1:], " · "))
}

// statusRows are the /agents details: one block per agent with a status.
func (m *Model) statusRows() []string {
	ids := make([]string, 0, len(m.statuses.byAgent))
	for id := range m.statuses.byAgent {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var rows []string
	for _, id := range ids {
		rows = append(rows, statusBlock(m.statuses.byAgent[id])...)
	}
	if m.statuses.truncated {
		rows = append(rows, fmt.Sprintf("(status list is partial: %d agents have a status)", m.statuses.total))
	}
	return rows
}

func statusBlock(st protocol.AgentStatus) []string {
	head := "◆ " + safe(st.AgentID) + "  " + st.State
	if st.Task != "" {
		head += " · " + safe(st.Task)
	}
	var detail []string
	if st.Step != "" {
		detail = append(detail, "step "+safe(st.Step))
	}
	if st.WaitingOn != "" {
		detail = append(detail, "waiting on "+st.WaitingOn)
	}
	if st.Ref != "" {
		detail = append(detail, "ref "+safe(st.Ref))
	}
	when := "declared " + age(st.DeclaredAt) + " ago · " + activityText(st)
	if !st.Connected {
		when += " · from a previous session (offline or replaced)"
	}
	rows := []string{agentStyle(st.AgentID).Render(head)}
	if len(detail) > 0 {
		rows = append(rows, "    "+strings.Join(detail, " · "))
	}
	return append(rows, mutedStyle.Render("    "+when))
}

// activityText names the observed activity and where it came from; without
// an exact-session observer it is unavailable, never guessed.
func activityText(st protocol.AgentStatus) string {
	if st.Activity == nil {
		return "activity unavailable"
	}
	return "activity " + age(st.Activity.At) + " ago (" + activitySource(st.Activity.Source) + ")"
}

func activitySource(source string) string {
	switch source {
	case "codex-launcher":
		return "launcher"
	case "claude-adapter":
		return "pairtask tool activity"
	}
	return safe(source)
}

// age is a short duration since t: <1m, 12m, 3h, 2d.
func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// footerStatuses bounds the agents the footer summarises.
const footerStatuses = 3

// statusSummary is the footer's compact view: current declarations only, as
// "id ▸ state age", at most footerStatuses agents and a count of the rest.
func (m *Model) statusSummary() string {
	var ids []string
	for id, st := range m.statuses.byAgent {
		if st.Connected {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var parts []string
	for _, id := range ids[:min(len(ids), footerStatuses)] {
		st := m.statuses.byAgent[id]
		parts = append(parts, safe(id)+" ▸ "+st.State+" "+age(st.DeclaredAt))
	}
	if more := len(ids) - footerStatuses; more > 0 {
		parts = append(parts, fmt.Sprintf("+%d", more))
	}
	return strings.Join(parts, " · ")
}
