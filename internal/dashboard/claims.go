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
		if set.Truncated {
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
	held := 0
	for _, set := range m.snapshot.Claims {
		for _, c := range set.Claims {
			if c.AgentID != agent || c.State == protocol.ClaimReleased {
				continue
			}
			held++
			lines = append(lines, fmt.Sprintf("  %s (%s, %s) on %s · %s ago · %s", safe(c.Task), safe(c.Role), safe(c.State),
				safe(set.OperatorMessageID[:min(len(set.OperatorMessageID), 8)]), m.age(c.ClaimedAt), optional(c.Topic)))
		}
	}
	if held == 0 {
		lines = append(lines, "  none held")
	}
	if m.claimsPartial() {
		lines = append(lines, "  partial: the snapshot is truncated, so claims may be omitted")
	}
	return lines
}
