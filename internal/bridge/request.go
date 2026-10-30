package bridge

import (
	"encoding/json"
	"errors"

	"github.com/pairtask/pairtask/internal/protocol"

	"github.com/google/uuid"
)

type SendRequest struct {
	To         string `json:"to" jsonschema:"Recipient agent ID, operator, or * for the public conversation (the other connected agents and the operator, protocol version 7)"`
	Text       string `json:"text" jsonschema:"Message text for the other agent"`
	MessageID  string `json:"message_id,omitempty" jsonschema:"Stable ID, maximum 128 bytes; generated when omitted. Retry durable sends with the same ID and identical content to inspect persisted outcome without rerouting"`
	InReplyTo  string `json:"in_reply_to,omitempty" jsonschema:"Message ID this message replies to"`
	NonDurable bool   `json:"non_durable,omitempty" jsonschema:"Explicitly use legacy delivery without replay; default false requires protocol version 3 and durable-capable recipients"`
	Room       string `json:"room,omitempty" jsonschema:"With to *, a room you joined: only its members and the operator see it; omit for the Square (everyone). A reply without room stays in its parent's room"`
	Workspace  string `json:"workspace,omitempty" jsonschema:"A paired workspace you may write to, by name or key from the workspaces tool: to is then one of its agents, or operator for its operator (protocol version 13)"`
}

func (r SendRequest) message() (protocol.Message, error) {
	if r.To == "" || r.Text == "" {
		return protocol.Message{}, errors.New("to and text are required")
	}
	if len(r.MessageID) > 128 || len(r.InReplyTo) > 128 {
		return protocol.Message{}, errors.New("message_id and in_reply_to must be at most 128 bytes")
	}
	if r.MessageID == "" {
		r.MessageID = uuid.NewString()
	}
	payload, err := json.Marshal(struct {
		Text string `json:"text"`
	}{r.Text})
	return protocol.Message{Type: protocol.TypeMsg, To: r.To, Payload: payload, MessageID: r.MessageID, InReplyTo: r.InReplyTo, AckRequested: true, Durable: !r.NonDurable, Room: r.Room, Workspace: r.Workspace}, err
}
