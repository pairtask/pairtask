package dashboard

import (
	"fmt"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func (m *model) details() string {
	rows := m.rows()
	if len(rows) == 0 {
		return m.emptyMessage() + "\n\n" + m.brokerDetails()
	}
	r := rows[m.cursor]
	lines := []string{"DETAILS", "", "Agent: " + safe(r.id.agent)}
	if r.session == nil {
		lines = append(lines, fmt.Sprintf("Pending recipient deliveries: %d", r.pending), "", "Count belongs to this logical inbox, across reconnects.",
			"Only committed agent acknowledgment clears pending delivery.", "A message may have been submitted to its host without acknowledgment.")
	} else {
		lines = append(lines, sessionDetails(r.session)...)
		lines = append(lines, m.statusDetails(r.session)...)
		lines = append(lines, m.claimDetails(r.session.AgentID)...)
	}
	if m.stale(time.Now()) {
		lines = append(lines, "", "STALE: these are retained observations, not current ownership.")
	}
	return strings.Join(lines, "\n") + "\n\n" + m.brokerDetails()
}

func (m *model) brokerDetails() string {
	lines := []string{"BROKER", "Health: " + safe(m.latest.Health),
		fmt.Sprintf("Socket reachable: %t", m.latest.Reachable),
		"Error: " + safe(m.latest.Error)}
	if m.haveSnapshot {
		lines = append(lines, m.freshness(), "Broker started: "+stamp(m.snapshot.BrokerStartedAt),
			fmt.Sprintf("History available: %t", m.snapshot.HistoryAvailable),
			fmt.Sprintf("Session limit: %d; truncated: %t", m.snapshot.SessionLimit, m.snapshot.SessionsTruncated))
		if p := m.snapshot.DurablePending; p != nil {
			lines = append(lines, fmt.Sprintf("Inbox limit: %d; truncated: %t", p.RecipientLimit, p.RecipientsTruncated))
		}
	}
	return strings.Join(lines, "\n")
}

func sessionDetails(s *protocol.SessionStatus) []string {
	return []string{
		"Session: " + safe(s.SessionID),
		"State: " + safe(s.State),
		"Connected: " + stamp(&s.ConnectedAt),
		"Last inbound: " + stamp(s.LastSeenAt),
		"Disconnected: " + stamp(s.DisconnectedAt),
		"Disconnect reason: " + optionalText(s.DisconnectReason),
		"",
		"Last inbound is registration or a processed frame, not model activity.",
		"Pending deliveries belong to the logical inbox; tab opens inbox counts.",
	}
}

func stateLabel(state string) string {
	if state == "transport_connected" {
		return "connected"
	}
	return safe(state)
}
func optionalText(s *string) string {
	if s == nil {
		return "unavailable"
	}
	return safe(*s)
}
func stamp(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "unavailable"
	}
	return t.UTC().Format(time.RFC3339)
}
func number(n *int) string {
	if n == nil {
		return "unavailable"
	}
	return fmt.Sprint(*n)
}

// operatorInbox shows unknown for older brokers, which don't report the inbox.
func operatorInbox(n *int) string {
	if n == nil {
		return "unknown"
	}
	return fmt.Sprint(*n)
}
func pendingTotal(p *protocol.PendingStatus) string {
	if p == nil {
		return "unavailable"
	}
	return fmt.Sprint(p.Total)
}

const helpText = `CONTROLS

↑/↓ or j/k   Select a row; scroll details or help
PgUp/PgDown  Move a page
Enter        Open / close full details
Tab          Switch sessions / pending inboxes
/            Filter by agent ID (Enter/Esc finish)
s            Cycle session state: all, connected, disconnected, stale
Esc          Close details/help and clear the text filter
r            Refresh now (ignored while a request is running)
?            Toggle this help
q / Ctrl+C   Quit; leave the broker and agents running

OBSERVATIONS

All rows describe a timestamped broker snapshot. Failed refreshes retain
old data with a STALE label. Degraded snapshots replace old counts with
unavailable values. Truncated lists may omit agents; absence does not
establish that an agent is offline or has no pending deliveries.

Connected means transport ownership. No model busy/idle or listener health
is inferred. Stale session rows lack a recorded disconnect and a live owner;
this differs from a STALE snapshot after failed or delayed refreshes.

Pending counts are recipient deliveries awaiting explicit acknowledgment,
including offline inboxes. Legacy/non-durable counts are unavailable.
This UI neither registers an agent nor reads or acknowledges messages.`
