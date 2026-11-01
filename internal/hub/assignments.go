package hub

import (
	"context"
	"slices"
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// Tags that assign (#278): an operator message to everyone or a room that
// tags agents assigns each its reply. Every tagged agent must be one of the
// message's frozen recipients and read assignments; otherwise the send is
// refused and nothing is stored but its rejected audit. The assignments are
// stored with the message, and each tagged agent's frame says so.

// assigns: the send's tags assign. A group's stay attention only.
func assigns(send operatorSend) bool {
	return send.scope.Kind == store.ScopeAll && len(send.msg.Mentions) > 0
}

// assignees are the agents the send assigns, none when it doesn't.
func assignees(send operatorSend) []string {
	if !assigns(send) {
		return nil
	}
	return send.msg.Mentions
}

// checkAssigned refuses the send's tags that can't be assigned, naming
// every one: outside its room, not connected, or too old to read it.
func (h *Hub) checkAssigned(ctx context.Context, clients map[string]*Client, recipients []protocol.Recipient, send operatorSend) *routingError {
	tagged := assignees(send)
	if len(tagged) == 0 {
		return nil
	}
	var members []string
	if send.msg.Room != "" {
		var err error
		if members, err = h.store.RoomMembers(ctx, send.msg.Room); err != nil {
			return &routingError{protocol.ErrInternal, "rooms unavailable"}
		}
	}
	return checkAssignees(assigneeSeat{clients, recipients, send.msg.Room, members}, tagged)
}

// assigneeSeat is what decides whether a tagged agent can be assigned: the
// connected clients, the frozen recipients, and a room's members ("" room
// for everyone).
type assigneeSeat struct {
	clients    map[string]*Client
	recipients []protocol.Recipient
	room       string
	members    []string
}

// checkAssignees refuses a tag naming an agent the message wouldn't reach,
// or one that can't read its assignment, naming every such tag.
func checkAssignees(seat assigneeSeat, tagged []string) *routingError {
	var problems []string
	for _, id := range tagged {
		if problem := seat.problem(id); problem != "" {
			problems = append(problems, "@"+id+" "+problem)
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return &routingError{protocol.ErrInvalidMention, strings.Join(problems, "; ")}
}

// problem is why a tagged agent can't be assigned, "" when it can.
func (s assigneeSeat) problem(id string) string {
	if s.room != "" && !slices.Contains(s.members, id) {
		return "isn't in #" + s.room
	}
	i := slices.IndexFunc(s.recipients, func(r protocol.Recipient) bool { return r.AgentID == id })
	if i < 0 {
		return "isn't connected; it would get nothing"
	}
	if c := sessionOwner(s.clients, s.recipients[i]); c == nil || c.ProtocolVersion < protocol.AssignVersion {
		return "needs a newer pairtask to be assigned"
	}
	return ""
}

// recipientFrame is the message as one recipient gets it: with its
// assignment when the tag assigned it.
func recipientFrame(out protocol.Message, agent string, assigned []string) protocol.Message {
	if slices.Contains(assigned, agent) {
		out.Assignment = &protocol.AssignmentView{Task: protocol.TaskReply, State: protocol.AssignmentAssigned}
	}
	return out
}

// checkNoAssignment: only the broker sets a frame's assignment.
func checkNoAssignment(msg protocol.Message) *routingError {
	if msg.Assignment != nil {
		return &routingError{protocol.ErrMalformedFrame, "assignment is set by the broker"}
	}
	return nil
}

// assignedReplyAudience keeps a reply to an assigned public message where
// it was asked: to everyone, in the parent's room. A reply to a message
// that assigns nobody keeps the rules it had.
func (h *Hub) assignedReplyAudience(ctx context.Context, msg protocol.Message) *routingError {
	if msg.InReplyTo == "" {
		return nil
	}
	assigned, room, err := h.store.AssignedAudience(ctx, msg.InReplyTo)
	if err != nil {
		return &routingError{protocol.ErrInternal, "assignments unavailable"}
	}
	if !assigned || msg.To == protocol.Broadcast && (msg.Room == "" || protocol.RoomScope(msg.Room) == room) {
		return nil
	}
	return &routingError{protocol.ErrInvalidReplyAudience, "answer where it was asked: to '*' in the parent's room"}
}

// forVersion is a claim page as a caller at version sees it: one before
// AssignVersion gets the claims exactly as before.
func forVersion(p *protocol.ClaimPage, version int) *protocol.ClaimPage {
	if p == nil || version >= protocol.AssignVersion {
		return p
	}
	out := *p
	out.AssignmentPage = nil
	return &out
}
