package chat

import (
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Assignments (#278): a tag in All or a room assigns its reply. They show
// with the claims: one line under the message naming each tagged agent and
// where it stands, a row each in /claims, and the agent's own in /agents.

// assignmentsOf is an operator message's assignments, from the snapshot.
func (m *Model) assignmentsOf(id string) []protocol.Assignment {
	page := m.claims.byMessage[id].AssignmentPage
	if page == nil {
		return nil
	}
	return page.Assignments
}

// assignmentSummary is the reply's line: "reply: @a assigned · @b done", or
// that nobody holds it once every assignee released it.
func assignmentSummary(assignments []protocol.Assignment) string {
	if len(assignments) == 0 {
		return ""
	}
	if allReleased(assignments) {
		return "nobody holds reply: all released"
	}
	parts := make([]string, len(assignments))
	for i, a := range assignments {
		parts[i] = "@" + safe(a.AgentID) + " " + a.State
	}
	return protocol.TaskReply + ": " + strings.Join(parts, " · ")
}

func allReleased(assignments []protocol.Assignment) bool {
	for _, a := range assignments {
		if a.State != protocol.AssignmentReleased {
			return false
		}
	}
	return true
}

// assignmentLines are the muted lines for a sent message's assignments,
// wrapped to the width.
func (m *Model) assignmentLines(e *entry, width int) []string {
	if e.direction != "sent" {
		return nil
	}
	summary := assignmentSummary(m.assignmentsOf(e.msg.MessageID))
	if summary == "" {
		return nil
	}
	var lines []string
	for _, line := range wrap("◇ "+summary, width) {
		lines = append(lines, mutedStyle.Render(line))
	}
	return lines
}

// assignmentRows are /claims rows: each assignee and its state with its
// age, then that nobody holds the reply when all released it.
func (m *Model) assignmentRows(id string) []string {
	assignments := m.assignmentsOf(id)
	var rows []string
	for _, a := range assignments {
		rows = append(rows, "  "+protocol.TaskReply+": @"+safe(a.AgentID)+" "+a.State+mutedStyle.Render(" · "+age(a.AssignedAt)))
	}
	if len(assignments) > 0 && allReleased(assignments) {
		rows = append(rows, "  nobody holds reply: all released")
	}
	return rows
}

// agentAssignmentLines are the assignments an agent holds, assigned or
// done, on the loaded operator messages.
func (m *Model) agentAssignmentLines(agent string) []string {
	var lines []string
	for id, page := range m.claims.byMessage {
		if page.AssignmentPage == nil {
			continue
		}
		for _, a := range page.Assignments {
			if a.AgentID == agent && a.State != protocol.AssignmentReleased {
				lines = append(lines, mutedStyle.Render("    ◇ "+protocol.TaskReply+" (assigned, "+a.State+") on "+m.parentLabel(id)))
			}
		}
	}
	return lines
}
