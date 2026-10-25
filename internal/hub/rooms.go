package hub

import (
	"context"
	"slices"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// Project rooms (#153): opt-in spaces for a group's discussion. A "*"
// without a room is the Square, everyone, as before; a "*" to a room reaches
// its v9 members and the operator. Direct messages work between anyone.

// joinHelloRooms adds the rooms a v9 hello names; joining is additive.
func (h *Hub) joinHelloRooms(ctx context.Context, c *Client) error {
	if c.ProtocolVersion < protocol.RoomVersion || len(c.JoinRooms) == 0 {
		return nil
	}
	members := make([]store.RoomMember, len(c.JoinRooms))
	for i, room := range c.JoinRooms {
		members[i] = store.RoomMember{Room: room, Agent: c.ID}
	}
	return h.store.JoinRooms(ctx, members...)
}

// noteHelloRooms tells the operator about rooms a v9 hello joined.
func (h *Hub) noteHelloRooms(c *Client) {
	if c.ProtocolVersion >= protocol.RoomVersion && len(c.JoinRooms) > 0 {
		h.emit(protocol.SessionEvent{Kind: protocol.EventRoomsChanged, AgentID: c.ID})
	}
}

// welcomeRooms adds the agent's rooms to a v9 welcome.
func (h *Hub) welcomeRooms(ctx context.Context, c *Client, welcome protocol.Message) protocol.Message {
	if c.ProtocolVersion < protocol.RoomVersion {
		return welcome
	}
	rooms, err := h.store.AgentRooms(ctx, c.ID)
	if err != nil {
		h.log.Error("rooms not read for welcome", "agent_id", c.ID, "error", err)
	}
	welcome.Rooms = rooms
	return welcome
}

// scopeAgentSend decides the room of an agent's "*": the one it names, else
// its parent's, else the Square. Other sends carry no room. Membership is
// checked later, for new sends only, so an accepted retry still reports.
func (h *Hub) scopeAgentSend(ctx context.Context, in *Inbound) *routingError {
	if in.Msg.To != protocol.Broadcast {
		in.Msg.Room = ""
		return nil
	}
	room, rerr := h.replyRoom(ctx, in.Msg)
	in.Msg.Room = room
	return rerr
}

// admitRoomPost refuses a new post to a room the sender isn't in.
func (h *Hub) admitRoomPost(ctx context.Context, in Inbound) *routingError {
	if in.Msg.Room == "" {
		return nil
	}
	rooms, err := h.store.AgentRooms(ctx, in.From.ID)
	if err != nil {
		return &routingError{protocol.ErrInternal, "rooms unavailable"}
	}
	if !slices.Contains(rooms, in.Msg.Room) {
		return &routingError{protocol.ErrRoomDenied, "join room " + in.Msg.Room + " before posting to it"}
	}
	return nil
}

// replyRoom is a "*" message's room. A reply without a room stays in its
// parent's; a reply naming another room, the Square included, is refused.
func (h *Hub) replyRoom(ctx context.Context, msg protocol.Message) (string, *routingError) {
	room := protocol.RoomScope(msg.Room)
	if msg.InReplyTo == "" {
		return room, nil
	}
	parent, err := h.store.MessageRoom(ctx, msg.InReplyTo)
	if err != nil {
		return "", &routingError{protocol.ErrInternal, "rooms unavailable"}
	}
	if msg.Room == "" {
		return parent, nil
	}
	if room != parent {
		return "", &routingError{protocol.ErrInvalidReply, "a reply stays in its parent's room"}
	}
	return room, nil
}

// roomAudience admits connected agents to a "*": everyone for the Square,
// a room's v9 members otherwise.
func (h *Hub) roomAudience(ctx context.Context, room string) (func(*Client) bool, *routingError) {
	if room == "" {
		return func(*Client) bool { return true }, nil
	}
	members, err := h.store.RoomMembers(ctx, room)
	if err != nil {
		return nil, &routingError{protocol.ErrInternal, "rooms unavailable"}
	}
	return func(c *Client) bool {
		return c.ProtocolVersion >= protocol.RoomVersion && slices.Contains(members, c.ID)
	}, nil
}

// roomFrames are an agent's room requests.
var roomFrames = map[string]bool{protocol.TypeRoomJoin: true, protocol.TypeRoomLeave: true, protocol.TypeRoomList: true}

func (h *Hub) handleRoom(ctx context.Context, clients map[string]*Client, in Inbound) {
	h.enqueue(clients, in.From, h.agentRoom(ctx, in))
}

// agentRoom joins, leaves or lists the agent's rooms and answers with its
// rooms afterwards, always read from the store.
func (h *Hub) agentRoom(ctx context.Context, in Inbound) protocol.Message {
	msg := in.Msg
	if in.From.ProtocolVersion < protocol.RoomVersion {
		return statusError(msg.RequestID, protocol.ErrMalformedFrame, "rooms require protocol_version 9")
	}
	if err := h.changeOwnRoom(ctx, in.From.ID, msg); err != nil {
		return statusError(msg.RequestID, err.code, err.detail)
	}
	rooms, err := h.store.AgentRooms(ctx, in.From.ID)
	if err != nil {
		return statusError(msg.RequestID, protocol.ErrInternal, "rooms unavailable")
	}
	return protocol.Message{Type: protocol.TypeRoomResult, RequestID: msg.RequestID, Rooms: rooms}
}

func (h *Hub) changeOwnRoom(ctx context.Context, agent string, msg protocol.Message) *routingError {
	if msg.Type == protocol.TypeRoomList {
		return nil
	}
	if err := protocol.ValidateRoomID(msg.Room); err != nil {
		return &routingError{protocol.ErrInvalidRoom, err.Error()}
	}
	var err error
	if msg.Type == protocol.TypeRoomJoin {
		err = h.store.JoinRooms(ctx, store.RoomMember{Room: msg.Room, Agent: agent})
	} else {
		err = h.store.LeaveRoom(ctx, store.RoomMember{Room: msg.Room, Agent: agent})
	}
	if err != nil {
		return &routingError{protocol.ErrInternal, "room change not stored"}
	}
	h.emit(protocol.SessionEvent{Kind: protocol.EventRoomsChanged, AgentID: agent})
	return nil
}

// scopeOperatorSend checks an operator message's room: only a "*" names one,
// and it must exist.
func (h *Hub) scopeOperatorSend(ctx context.Context, msg *protocol.Message, scope store.OperatorScope) *routingError {
	if scope.Kind != store.ScopeAll {
		if protocol.RoomScope(msg.Room) != "" {
			return &routingError{protocol.ErrMalformedFrame, "room applies to \"*\" only"}
		}
		msg.Room = ""
		return nil
	}
	room, rerr := h.replyRoom(ctx, *msg) // a reply stays in its parent's room
	if rerr != nil {
		return rerr
	}
	msg.Room = room
	if room == "" {
		return nil
	}
	exists, err := h.store.RoomExists(ctx, msg.Room)
	if err != nil {
		return &routingError{protocol.ErrInternal, "rooms unavailable"}
	}
	if !exists {
		return &routingError{protocol.ErrInvalidRoom, "no room " + msg.Room}
	}
	return nil
}

// operatorRoom serves operator_room: list, create, add and remove, each
// audited; the answer is every room afterwards.
func (h *Hub) operatorRoom(ctx context.Context, _ map[string]*Client, req operatorRequest) protocol.Message {
	msg := req.msg
	auditID, err := h.store.RecordOperatorAudit(ctx, store.OperatorAudit{Action: "room " + msg.RoomOp, PrincipalUID: req.uid, MessageID: msg.MessageID, Scope: msg.Room, Result: store.AuditPending})
	if err != nil {
		return (&routingError{protocol.ErrInternal, "operator audit unavailable; rooms unchanged"}).frame(msg.MessageID)
	}
	if rerr := h.changeRooms(ctx, msg); rerr != nil {
		h.finishAudit(ctx, auditID, store.AuditOutcome{Result: store.AuditRejected, Detail: rerr.code})
		return rerr.frame(msg.MessageID)
	}
	list, err := h.store.Rooms(ctx)
	if err != nil {
		h.finishAudit(ctx, auditID, store.AuditOutcome{Result: store.AuditRejected, Detail: protocol.ErrInternal})
		return (&routingError{protocol.ErrInternal, "rooms unavailable"}).frame(msg.MessageID)
	}
	h.finishAudit(ctx, auditID, store.AuditOutcome{Result: store.AuditAccepted})
	return protocol.Message{Type: protocol.TypeOperatorRoomResult, MessageID: msg.MessageID, RoomList: &list}
}

func (h *Hub) changeRooms(ctx context.Context, msg protocol.Message) *routingError {
	op, ok := roomOps[msg.RoomOp]
	if !ok {
		return &routingError{protocol.ErrMalformedFrame, "room_op must be list, create, add or remove"}
	}
	if rerr := op.check(msg); rerr != nil {
		return rerr
	}
	if err := op.apply(h, ctx, msg); err != nil {
		return &routingError{protocol.ErrInternal, "room change not stored"}
	}
	if msg.RoomOp != protocol.RoomOpList {
		h.emit(protocol.SessionEvent{Kind: protocol.EventRoomsChanged, AgentID: msg.AgentID})
	}
	return nil
}

type roomOp struct {
	check func(protocol.Message) *routingError
	apply func(h *Hub, ctx context.Context, msg protocol.Message) error
}

var roomOps = map[string]roomOp{
	protocol.RoomOpList: {noRoomArgs, func(*Hub, context.Context, protocol.Message) error { return nil }},
	protocol.RoomOpCreate: {roomArg, func(h *Hub, ctx context.Context, msg protocol.Message) error {
		_, err := h.store.CreateRoom(ctx, msg.Room)
		return err
	}},
	protocol.RoomOpAdd: {memberArgs, func(h *Hub, ctx context.Context, msg protocol.Message) error {
		return h.store.JoinRooms(ctx, store.RoomMember{Room: msg.Room, Agent: msg.AgentID})
	}},
	protocol.RoomOpRemove: {memberArgs, func(h *Hub, ctx context.Context, msg protocol.Message) error {
		return h.store.LeaveRoom(ctx, store.RoomMember{Room: msg.Room, Agent: msg.AgentID})
	}},
}

func noRoomArgs(msg protocol.Message) *routingError {
	if msg.Room != "" || msg.AgentID != "" {
		return &routingError{protocol.ErrMalformedFrame, "list takes no room or agent_id"}
	}
	return nil
}

func roomArg(msg protocol.Message) *routingError {
	if err := protocol.ValidateRoomID(msg.Room); err != nil {
		return &routingError{protocol.ErrInvalidRoom, err.Error()}
	}
	return nil
}

func memberArgs(msg protocol.Message) *routingError {
	if err := protocol.ValidateAgentID(msg.AgentID); err != nil {
		return &routingError{protocol.ErrMalformedFrame, err.Error()}
	}
	return roomArg(msg)
}
