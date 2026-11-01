package chat

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

// A paired workspace's messages read like local ones (#275): the time and
// who wrote to whom, the message a reply answers, the body, then one muted
// line each for the review, the outbound state and every acknowledgment.
// No message number shows: a contact's sequences are per direction and
// would read as local ones.

// contactSides names a message's two ends as this side shows them: ours as
// "you" or a bare agent ID, theirs as "⇄ WORKSPACE" or AGENT@WORKSPACE, so
// their agents never read as ours.
type contactSides struct {
	c  protocol.PairingContactView
	ws workspaceLabel
}

func sidesOf(c protocol.PairingContactView) contactSides {
	return contactSides{c: c, ws: workspaceTag(c)}
}

func (s contactSides) ours(agents []string) string {
	return agentNames(agents, "you")
}

func (s contactSides) theirs(agents []string) string {
	if len(agents) == 0 {
		return "⇄ " + safe(contactDisplay(s.c))
	}
	return theirAgents(agents, s.ws)
}

// sender is who wrote v, recipients whom it is to: received, the sender is
// theirs and the recipients ours; sent, the reverse.
func (s contactSides) sender(v protocol.ContactMessageView) string {
	var from []string
	if v.From != "" {
		from = []string{v.From}
	}
	if v.Direction == "in" {
		return s.theirs(from)
	}
	return s.ours(from)
}

func (s contactSides) recipients(v protocol.ContactMessageView) string {
	if v.Direction == "in" {
		return s.ours(v.To)
	}
	return s.theirs(v.To)
}

// messageHead is "HH:MM  FROM → TO", marked [remote] when received, wrapped
// rather than cut when the tab is narrow.
func (m *Model) messageHead(s contactSides, v protocol.ContactMessageView) []string {
	head := mutedStyle.Render(v.CreatedAt.Local().Format("15:04")+"  ") +
		s.senderStyled(v) + " → " + s.recipients(v)
	if v.Direction == "in" {
		head += mutedStyle.Render(" [remote]")
	}
	if ansi.StringWidth(head) <= m.transcriptWidth() {
		return []string{head}
	}
	return wrap(head, m.transcriptWidth())
}

// senderStyled colors the sender as local chat does: this side's operator
// as you, any agent or their operator in its own color.
func (s contactSides) senderStyled(v protocol.ContactMessageView) string {
	name := s.sender(v)
	switch {
	case v.Direction == "in":
		return agentStyle(s.c.IdentityKey).Render(name)
	case v.From == "":
		return youStyle.Render(name)
	}
	return agentStyle(v.From).Render(name)
}

// replyParent is the message v answers in this contact's loaded thread:
// the same wire ID can be one sent and one received, so the side the reply
// names decides which, and a side it doesn't name decides nothing. ok is
// false when none or more than one match.
func replyParent(v protocol.ContactMessageView, thread []protocol.ContactMessageView) (protocol.ContactMessageView, bool) {
	direction, ok := parentDirection(v)
	if !ok {
		return protocol.ContactMessageView{}, false
	}
	var found []protocol.ContactMessageView
	for _, p := range thread {
		if p.ID == v.ReplyID && p.Direction == direction {
			found = append(found, p)
		}
	}
	if len(found) != 1 {
		return protocol.ContactMessageView{}, false
	}
	return found[0], true
}

// parentDirection: a parent by the reply's sender was written on the same
// side as the reply, one by its receiver on the other.
func parentDirection(v protocol.ContactMessageView) (string, bool) {
	switch v.ReplySide {
	case protocol.ReplyBySender:
		return v.Direction, true
	case protocol.ReplyByReceiver:
		if v.Direction == "in" {
			return "out", true
		}
		return "in", true
	}
	return "", false
}

// replyRows is "↳ re HH:MM FROM" for a reply whose parent is loaded, else
// "↳ re an earlier message"; nothing for a message that answers none.
func (m *Model) replyRows(s contactSides, v protocol.ContactMessageView, thread []protocol.ContactMessageView) []string {
	if v.ReplyID == "" {
		return nil
	}
	text := "↳ re an earlier message"
	if p, ok := replyParent(v, thread); ok {
		text = "↳ re " + p.CreatedAt.Local().Format("15:04") + " " + s.sender(p)
	}
	return m.mutedRows([]string{text})
}

// messageStatusRows are the review, the outbound state and each acknowledgment,
// one muted line each, in that order.
func (m *Model) messageStatusRows(s contactSides, v protocol.ContactMessageView) []string {
	var rows []string
	if review := strings.TrimPrefix(reviewHead(v), " · "); review != "" {
		rows = append(rows, review)
	}
	if v.Direction == "out" {
		rows = append(rows, m.outboundState(v))
	}
	for _, a := range v.AgentAcks {
		rows = append(rows, "· "+theirAgents([]string{a}, s.ws)+": acknowledged")
	}
	return m.mutedRows(rows)
}

// mutedRows indents texts as a body, each wrapped at the tab's width.
func (m *Model) mutedRows(texts []string) []string {
	var out []string
	for _, text := range texts {
		for _, part := range wrap(text, m.transcriptWidth()-len(body)) {
			out = append(out, string(body)+mutedStyle.Render(part))
		}
	}
	return out
}
