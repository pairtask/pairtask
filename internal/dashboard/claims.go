package dashboard

import (
	"fmt"

	"github.com/pairtask/pairtask/internal/protocol"
)

// claimsMetric is the header's claim count: active claims in the loaded
// sets, marked "+" when the snapshot may have left some out.
func (m *model) claimsMetric() string {
	if !m.snapshot.ClaimsAvailable {
		return "Claims unavailable"
	}
	active := 0
	for _, set := range m.snapshot.Claims {
		for _, c := range set.Claims {
			if c.State == protocol.ClaimActive {
				active++
			}
		}
	}
	more := ""
	if m.claimsPartial() {
		more = "+"
	}
	return fmt.Sprintf("Active claims %d%s", active, more)
}

// claimsPartial is whether the snapshot left claimed messages out, or a
// message left claims out.
func (m *model) claimsPartial() bool {
	if m.snapshot.ClaimsTruncated {
		return true
	}
	for _, set := range m.snapshot.Claims {
		if set.Truncated || set.AssignmentPage != nil && set.AssignmentsTruncated {
			return true
		}
	}
	return false
}

// claimDetails are the claims the row's agent holds, active or done; claims
// belong to the agent, not to one session.
func (m *model) claimDetails(agent string) []string {
	if !m.snapshot.ClaimsAvailable {
		return []string{"", "Claims: unavailable"}
	}
	lines := []string{"", "Claims (a claim coordinates agents; it is never an approval):"}
	held := len(lines)
	for _, set := range m.snapshot.Claims {
		for _, c := range set.Claims {
			if c.AgentID != agent || c.State == protocol.ClaimReleased {
				continue
			}
			lines = append(lines, fmt.Sprintf("  %s (%s, %s) on %s · %s ago · %s", safe(c.Task), safe(c.Role), safe(c.State),
				parentPrefix(set.OperatorMessageID), m.age(c.ClaimedAt), optional(c.Topic)))
		}
		lines = append(lines, m.assignmentDetails(set, agent)...)
	}
	if len(lines) == held {
		lines = append(lines, "  none held")
	}
	if m.claimsPartial() {
		lines = append(lines, "  partial: the snapshot is truncated, so claims may be omitted")
	}
	return lines
}

// assignmentDetails are the reply a tag assigned the agent on one message
// (#278), assigned or done.
func (m *model) assignmentDetails(set protocol.ClaimSet, agent string) []string {
	if set.AssignmentPage == nil {
		return nil
	}
	var lines []string
	for _, a := range set.Assignments {
		if a.AgentID == agent && a.State != protocol.AssignmentReleased {
			lines = append(lines, fmt.Sprintf("  %s (assigned, %s) on %s · %s ago", safe(a.Task), safe(a.State),
				parentPrefix(set.OperatorMessageID), m.age(a.AssignedAt)))
		}
	}
	return lines
}

// parentPrefix is an operator message ID's start, as rows name it.
func parentPrefix(id string) string {
	return safe(id[:min(len(id), 8)])
}
