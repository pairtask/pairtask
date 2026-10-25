package chat

import (
	"fmt"
	"sort"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Task claims (#141): which agent owns or reviews which task of an operator
// message. They arrive with the snapshot, refresh on claims_changed like
// statuses do, and are shown under the message. A claim is never an
// approval, and showing one never sends or acknowledges anything.

const panelClaims = "claims"

// claimView is the latest claims by operator message, from the snapshot.
type claimView struct {
	byMessage map[string]protocol.ClaimPage // each message's claims with its own total and truncation
	total     int                           // operator messages with claims
	truncated bool                          // the snapshot left some messages out
}

func (c *claimView) set(snap *protocol.OperatorSnapshot) {
	c.byMessage = make(map[string]protocol.ClaimPage, len(snap.Claims))
	for _, set := range snap.Claims {
		page := set.ClaimPage
		page.Claims = sortedClaims(set.Claims)
		c.byMessage[set.OperatorMessageID] = page
	}
	c.total, c.truncated = snap.ClaimsTotal, snap.ClaimsTruncated
}

// sortedClaims orders held claims first, then by task and role.
func sortedClaims(claims []protocol.Claim) []protocol.Claim {
	out := append([]protocol.Claim(nil), claims...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := out[i].State == protocol.ClaimReleased, out[j].State == protocol.ClaimReleased
		if ri != rj {
			return !ri
		}
		if out[i].Task != out[j].Task {
			return out[i].Task < out[j].Task
		}
		return out[i].Role == protocol.RoleOwner && out[j].Role != protocol.RoleOwner
	})
	return out
}

// claimsOf is an operator message's claims; only messages the operator sent
// can carry them.
func (m *Model) claimsOf(e *entry) []protocol.Claim {
	if e.direction != "sent" {
		return nil
	}
	return m.claims.byMessage[e.msg.MessageID].Claims
}

// claimLines are the muted lines under a message, one per held claim,
// wrapped to the width so a narrow view never cuts the role or state.
func (m *Model) claimLines(e *entry, width int) []string {
	var lines []string
	for _, c := range m.claimsOf(e) {
		if c.State == protocol.ClaimReleased {
			continue
		}
		for _, line := range wrap("◇ "+claimText(c), width) {
			lines = append(lines, mutedStyle.Render(line))
		}
	}
	return lines
}

func claimText(c protocol.Claim) string {
	return fmt.Sprintf("%s: %s (%s, %s)", safe(c.Task), safe(c.AgentID), c.Role, c.State)
}

// claimsCommand opens the claims of a sent message: every claim with its
// age, and an explicit note when the snapshot may have left this one out.
func (m *Model) claimsCommand(arg argument) tea.Cmd {
	e, reason := m.target(arg, noEntry)
	if e != nil && e.direction != "sent" {
		e, reason = nil, "claims exist only on messages you sent"
	}
	if e == nil {
		m.status = reason
		return nil
	}
	m.panel = &panel{kind: panelClaims, seq: e.msg.Seq, id: e.msg.MessageID}
	return nil
}

func (m *Model) claimRows() []string {
	rows := []string{titleStyle.Render(fmt.Sprintf("Claims on #%d", m.panel.seq)) + mutedStyle.Render("  (Esc closes)")}
	page, loaded := m.claims.byMessage[m.panel.id]
	claims := page.Claims
	for _, c := range claims {
		row := "  " + claimText(c)
		if topic := safe(c.Topic); topic != "" {
			row += " · " + topic
		}
		rows = append(rows, row+mutedStyle.Render(" · "+age(c.ClaimedAt)))
	}
	switch {
	case page.Truncated:
		rows = append(rows, fmt.Sprintf("  showing %d of %d claims (truncated: held claims first, older released ones left out)", len(claims), page.Total))
	case loaded:
	case m.claims.truncated:
		rows = append(rows, "  claims not loaded: the snapshot is truncated to the newest messages with claims")
	default:
		rows = append(rows, "  no claims")
	}
	return append(rows, mutedStyle.Render("  a claim coordinates agents; it is never an approval"))
}

// agentClaimLines are the claims an agent holds, active or done, on the
// loaded operator messages: "◇ task (role, state) on #seq · topic", or the
// message ID's start when the message isn't loaded.
func (m *Model) agentClaimLines(agent string) []string {
	var held []protocol.Claim
	for _, page := range m.claims.byMessage {
		for _, c := range page.Claims {
			if c.AgentID == agent && c.State != protocol.ClaimReleased {
				held = append(held, c)
			}
		}
	}
	sort.Slice(held, func(i, j int) bool {
		if held[i].OperatorMessageID != held[j].OperatorMessageID {
			return held[i].OperatorMessageID < held[j].OperatorMessageID
		}
		return held[i].Task < held[j].Task
	})
	lines := make([]string, 0, len(held))
	for _, c := range held {
		line := "    ◇ " + safe(c.Task) + " (" + c.Role + ", " + c.State + ") on " + m.parentLabel(c.OperatorMessageID)
		if topic := safe(c.Topic); topic != "" {
			line += " · " + topic
		}
		lines = append(lines, mutedStyle.Render(line))
	}
	return lines
}

// parentLabel names an operator message by its #seq when loaded.
func (m *Model) parentLabel(id string) string {
	if e := m.timeline.entries[id]; e != nil {
		return fmt.Sprintf("#%d", e.msg.Seq)
	}
	return safe(id[:min(len(id), 8)])
}

// claimsPartialNote says when the agents' claims may be incomplete: the
// snapshot left messages out, or a message left claims out.
func (m *Model) claimsPartialNote() string {
	if m.claims.truncated {
		return fmt.Sprintf("(claims are partial: shown for the newest messages, %d have claims)", m.claims.total)
	}
	for _, page := range m.claims.byMessage {
		if page.Truncated {
			return "(claims are partial: some messages have more claims than shown)"
		}
	}
	return ""
}
