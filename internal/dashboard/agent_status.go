package dashboard

import (
	"fmt"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/status"
)

// declaration is the status a session row shows: the record its own session
// declared, or, on the agent's live row, one an earlier session declared.
// Any other historical row of the agent shows none.
func (m *model) declaration(s *protocol.SessionStatus) (rec *protocol.AgentStatus, previous bool) {
	for i := range m.snapshot.AgentStatus {
		r := &m.snapshot.AgentStatus[i]
		if r.AgentID != s.AgentID {
			continue
		}
		switch {
		case r.SessionID == s.SessionID:
			return r, !r.Connected
		case s.State == "transport_connected":
			return r, true
		}
		return nil, false
	}
	return nil, false
}

// declared reports whether the snapshot has any record for the agent.
func (m *model) declared(agent string) bool {
	for _, r := range m.snapshot.AgentStatus {
		if r.AgentID == agent {
			return true
		}
	}
	return false
}

// statusKnown is whether a missing record means "none declared": only for a
// complete, available list.
func (m *model) statusKnown() bool {
	return m.snapshot.AgentStatusAvailable && !m.snapshot.AgentStatusTruncated
}

// statusCell is a session row's status column.
func (m *model) statusCell(s *protocol.SessionStatus) string {
	rec, previous := m.declaration(s)
	switch {
	case rec != nil && previous && rec.SessionID != s.SessionID:
		return "previous: " + safe(rec.State) + " · " + m.age(rec.DeclaredAt)
	case rec != nil:
		return safe(rec.State)
	case m.declared(s.AgentID):
		return "—"
	case m.statusKnown():
		return "no status"
	}
	return "unknown"
}

// statusDetails describes the row's declaration as declared, with its ages;
// nothing is inferred from age or from a disconnect.
func (m *model) statusDetails(s *protocol.SessionStatus) []string {
	rec, previous := m.declaration(s)
	if rec == nil {
		return []string{"", "Agent status: " + m.statusCell(s)}
	}
	session := "current session"
	if previous {
		session = "previous session " + safe(rec.SessionID)
	}
	return []string{"",
		"Agent status: " + safe(rec.State) + " (" + session + ")",
		"Task: " + safe(rec.Task),
		"Step: " + safe(rec.Step),
		"Waiting on: " + optional(rec.WaitingOn),
		"Ref: " + optional(rec.Ref),
		"Declared: " + stamp(&rec.DeclaredAt) + " (" + m.age(rec.DeclaredAt) + " ago)",
		"Activity: " + m.activity(rec.Activity),
	}
}

func (m *model) activity(a *protocol.Activity) string {
	if a == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%s ago via %s", m.age(a.At), safe(a.Source))
}

// age is relative to the snapshot, so a retained snapshot keeps its ages.
func (m *model) age(t time.Time) string {
	now := time.Now()
	if m.snapshot.SnapshotAt != nil {
		now = *m.snapshot.SnapshotAt
	}
	return status.Age(now, t)
}

func optional(s string) string {
	if s == "" {
		return "—"
	}
	return safe(s)
}
