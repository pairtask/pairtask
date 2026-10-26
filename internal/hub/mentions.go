package hub

import (
	"context"
	"slices"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// Mentions (#164) mark the members of a room or group an operator message
// addresses. They are attention, not routing: the audience stays the room or
// the group. The shape is checked before the retry lookup, and room
// membership after it, so a retry of an accepted message still gets its
// outcome after members leave or the room goes.

// canonicalMentions sorts and deduplicates the mentions, and refuses them
// outside a room or group, beyond the cap, or outside a group's members.
func canonicalMentions(msg *protocol.Message, scope store.OperatorScope) *routingError {
	if len(msg.Mentions) == 0 {
		msg.Mentions = nil
		return nil
	}
	if !mentionScope(*msg, scope) {
		return &routingError{protocol.ErrMalformedFrame, "mentions need a room or a group"}
	}
	ids := slices.Clone(msg.Mentions)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) > protocol.MaxMentions {
		return &routingError{protocol.ErrInvalidMention, "at most 16 mentions"}
	}
	for _, id := range ids {
		if rerr := mentionInScope(id, scope); rerr != nil {
			return rerr
		}
	}
	msg.Mentions = ids
	return nil
}

func mentionScope(msg protocol.Message, scope store.OperatorScope) bool {
	if scope.Kind == store.ScopeAll {
		return msg.Room != ""
	}
	return scope.Kind == store.ScopeGroup && len(scope.Members) > 1
}

func mentionInScope(id string, scope store.OperatorScope) *routingError {
	if err := protocol.ValidateAgentID(id); err != nil {
		return &routingError{protocol.ErrMalformedFrame, err.Error()}
	}
	if scope.Kind == store.ScopeGroup && !slices.Contains(scope.Members, id) {
		return &routingError{protocol.ErrInvalidMention, id + " is not in the group"}
	}
	return nil
}

// requireMentionedMembers refuses a new room message mentioning someone who
// isn't in the room now.
func (h *Hub) requireMentionedMembers(ctx context.Context, msg protocol.Message) *routingError {
	if msg.Room == "" || len(msg.Mentions) == 0 {
		return nil
	}
	members, err := h.store.RoomMembers(ctx, msg.Room)
	if err != nil {
		return &routingError{protocol.ErrInternal, "rooms unavailable"}
	}
	for _, id := range msg.Mentions {
		if !slices.Contains(members, id) {
			return &routingError{protocol.ErrInvalidMention, id + " is not in #" + msg.Room}
		}
	}
	return nil
}
