// Package protocol defines the NDJSON wire format between agents and the broker.
// Each frame is a single JSON object terminated by a newline.
package protocol

import (
	"encoding/json"
	"errors"
	"time"
)

// Frame types.
const (
	TypeHello   = "hello"   // client -> broker, must be the first frame
	TypeWelcome = "welcome" // broker -> client, confirms registration
	TypeMsg     = "msg"     // bidirectional, chat payload
	TypeError   = "error"   // broker -> client, protocol or routing error
	TypeAck     = "ack"     // client -> broker receipt; broker -> client persisted acknowledgment

	// Operator socket only: one request per connection.
	TypeOperatorSend        = "operator_send"
	TypeOperatorReceive     = "operator_receive"      // request a page of the operator inbox
	TypeOperatorReceived    = "operator_received"     // confirm the client adopted received messages
	TypeOperatorAck         = "operator_ack"          // the human's explicit receipt of one message
	TypeOperatorAckMany     = "operator_ack_many"     // the human's explicit receipt of a stable page of messages
	TypeOperatorInboxStatus = "operator_inbox_status" // request and response: inbox usage against its bounds
	TypeOperatorPage        = "operator_page"         // response to operator_receive

	// Live operator session.
	TypeOperatorSessionOpen   = "operator_session_open"
	TypeOperatorSession       = "operator_session" // response to operator_session_open
	TypeOperatorSnapshot      = "operator_snapshot"
	TypeOperatorMessageStages = "operator_message_stages"
	TypeOperatorHistory       = "operator_history" // request and response
	TypeEvent                 = "event"
)

const (
	Version                = AgentStatusVersion
	AcknowledgmentVersion  = 2
	DurableVersion         = 3
	OriginVersion          = 4
	OperatorInboxVersion   = 5
	AgentStatusVersion     = 6 // agent_status_set/get/result and agent_activity
	StageAccepted          = "accepted"
	StageAdapterReceived   = "adapter_received"
	StageAgentAcknowledged = "agent_acknowledged"
)

// Origin is set by the broker from the connection, never accepted from a client.
const (
	OriginOperator = "operator"
	OriginAgent    = "agent"
	OriginUnknown  = "unknown"
)

// Broadcast is the reserved recipient for messages to all connected agents.
const Broadcast = "*"

// OperatorID is the reserved sender of operator messages; agents cannot claim it.
const OperatorID = "operator"

// MaxRecipients bounds broadcast and group membership.
const MaxRecipients = 256

// ValidateAgentID is shared by wire and MCP registration.
func ValidateAgentID(id string) error {
	if len(id) == 0 || len(id) > 128 {
		return errors.New("agent ID must be 1 to 128 bytes")
	}
	if id == Broadcast {
		return errors.New("agent ID cannot be *")
	}
	if id == OperatorID {
		return errors.New("agent ID operator is reserved")
	}
	return nil
}

// WithDisplayOrigin labels a delivered msg frame for a host; other frame types
// carry no origin.
func WithDisplayOrigin(msg Message) Message {
	if msg.Type == TypeMsg {
		msg.Origin = DisplayOrigin(msg.Origin)
	}
	return msg
}

// DisplayOrigin renders a missing origin as unknown, never as operator or agent.
func DisplayOrigin(origin string) string {
	switch origin {
	case OriginOperator, OriginAgent:
		return origin
	}
	return OriginUnknown
}

// Error codes sent in TypeError frames.
const (
	ErrExpectedHello         = "expected_hello"
	ErrDuplicateID           = "duplicate_id"
	ErrUnknownRecipient      = "unknown_recipient"
	ErrRecipientUnavailable  = "recipient_unavailable"
	ErrRecipientDisconnected = "recipient_disconnected"
	ErrMalformedFrame        = "malformed_frame"
	ErrMissingRecipient      = "missing_recipient"
	ErrInternal              = "internal"
	ErrDuplicateMessage      = "duplicate_message"
	ErrInvalidAck            = "invalid_ack"
	ErrInboxFull             = "inbox_full"
	ErrDurabilityUnavailable = "durability_unavailable"
	ErrForbidden             = "forbidden"
	ErrInvalidReply          = "invalid_reply"
	ErrUnsupportedOperation  = "unsupported_operation"
	ErrOwnerBusy             = "owner_busy"
	ErrInvalidState          = "invalid_state"
	ErrInvalidMessage        = "invalid_message"
)

// Recipient freezes broadcast membership at acceptance, including inbox ownership.
type Recipient struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
}

// ReceiptState is retained per-recipient progress returned on an idempotent retry.
type ReceiptState struct {
	MessageID string `json:"message_id,omitempty"` // operator_received: the confirmed message
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id,omitempty"`
	Stage     string `json:"stage"`
}

// Message is the single frame type used in both directions.
// Fields are populated depending on Type.
type Message struct {
	Status          *StatusSnapshot   `json:"status,omitempty"` // status response only
	Type            string            `json:"type"`
	ProtocolVersion int               `json:"protocol_version,omitempty"` // hello/welcome capability negotiation
	MessageID       string            `json:"message_id,omitempty"`
	InReplyTo       string            `json:"in_reply_to,omitempty"`
	AckRequested    bool              `json:"ack_requested,omitempty"`    // msg: request staged acknowledgments
	Durable         bool              `json:"durable,omitempty"`          // v3: retained until explicit agent acknowledgment
	Replayed        bool              `json:"replayed,omitempty"`         // delivery on a replacement connection
	Repeated        bool              `json:"repeated,omitempty"`         // accepted: idempotent send retry; no new delivery
	Stage           string            `json:"stage,omitempty"`            // ack
	Recipients      []Recipient       `json:"recipients,omitempty"`       // accepted: fixed membership
	Receipts        []ReceiptState    `json:"receipts,omitempty"`         // repeated acceptance: latest retained progress
	Seq             uint64            `json:"seq,omitempty"`              // broker-assigned global order (msg, welcome)
	AgentID         string            `json:"agent_id,omitempty"`         // hello, welcome
	SessionID       string            `json:"session_id,omitempty"`       // broker-assigned connection identity
	OwnerSessionID  string            `json:"owner_session_id,omitempty"` // duplicate_id: current inbox owner
	Harness         string            `json:"harness,omitempty"`          // hello: e.g. "claude-code 1.5"
	AgentStatus     *AgentStatus      `json:"agent_status,omitempty"`     // agent_status_set request and agent_status_result
	Source          string            `json:"source,omitempty"`           // agent_activity: who observed it
	Model           string            `json:"model,omitempty"`            // hello: e.g. "claude-sonnet-4"
	From            string            `json:"from,omitempty"`             // msg (broker -> client)
	To              string            `json:"to,omitempty"`               // msg: agent id or "*"
	Origin          string            `json:"origin,omitempty"`           // msg (broker -> client): operator, agent or unknown
	Group           []string          `json:"group,omitempty"`            // operator_send: explicit recipient set
	Excluded        []string          `json:"excluded,omitempty"`         // operator accepted: connected agents left out of "*"
	PageRequest     *PageRequest      `json:"page_request,omitempty"`     // operator_receive
	Page            *OperatorPage     `json:"page,omitempty"`             // operator_page
	MessageIDs      []string          `json:"message_ids,omitempty"`      // operator_received
	AckSelection    *AckSelection     `json:"ack_selection,omitempty"`    // operator_ack_many
	AckMany         *AckManyResult    `json:"ack_many,omitempty"`         // operator_ack_many response
	Inbox           *InboxStatus      `json:"inbox,omitempty"`            // operator_inbox_status response
	RequestID       string            `json:"request_id,omitempty"`       // operator session requests and their responses
	Client          string            `json:"client,omitempty"`           // operator_session_open
	Session         *SessionInfo      `json:"session,omitempty"`          // operator_session
	Owner           *SessionInfo      `json:"owner,omitempty"`            // owner_busy
	Snapshot        *OperatorSnapshot `json:"snapshot,omitempty"`         // operator_snapshot response
	Stages          *StagePage        `json:"stages,omitempty"`           // operator_message_stages response
	Event           *SessionEvent     `json:"event,omitempty"`            // event
	AfterAgent      string            `json:"after_agent,omitempty"`      // operator_message_stages
	Limit           int               `json:"limit,omitempty"`            // operator_message_stages
	History         *HistoryPage      `json:"history,omitempty"`          // operator_history response
	Payload         json.RawMessage   `json:"payload,omitempty"`          // msg: opaque JSON
	Code            string            `json:"code,omitempty"`             // error
	Detail          string            `json:"detail,omitempty"`           // error
	TS              *time.Time        `json:"ts,omitempty"`               // msg (broker -> client), UTC; pointer so zero is omitted
}

// Operator inbox paging limits.
const (
	OperatorPageDefault = 16
	OperatorPageMax     = 32
	AckManyDefault      = 64
	AckManyMax          = 256
)

// AckSelection is a stable page of operator-inbox rows to acknowledge: every
// row with AfterSeq < seq <= UpperSeq (from one sender when From is set),
// acknowledged or not, in sequence order, at most Limit rows. Retrying the
// same selection re-examines the same rows.
type AckSelection struct {
	From     string `json:"from,omitempty"`
	AfterSeq uint64 `json:"after_seq"`
	UpperSeq uint64 `json:"upper_seq"`
	Limit    int    `json:"limit,omitempty"` // omitted or 0 means AckManyDefault
}

// AckManyResult reports what one operator_ack_many page did. Skipped rows
// were not yet received and stay untouched; a later run of the same scope
// acknowledges them once they are.
type AckManyResult struct {
	Acknowledged        int    `json:"acknowledged"`
	AlreadyAcknowledged int    `json:"already_acknowledged"`
	Skipped             int    `json:"skipped"`
	NextAfterSeq        uint64 `json:"next_after_seq"`
	More                bool   `json:"more"`
}

// InboxStatus is the operator inbox's usage against its bounds. Unreceived
// rows are a subset of unacknowledged ones.
type InboxStatus struct {
	HeadSeq                 uint64        `json:"head_seq"`
	OldestUnacknowledgedSeq uint64        `json:"oldest_unacknowledged_seq"`
	Unacknowledged          int           `json:"unacknowledged"`
	Unreceived              int           `json:"unreceived"`
	Bytes                   int           `json:"bytes"`
	Limits                  InboxLimits   `json:"limits"`
	Senders                 []SenderUsage `json:"senders"`
	Retained                RetainedUsage `json:"retained"`
}

type InboxLimits struct {
	SenderMessages int `json:"sender_messages"`
	SenderBytes    int `json:"sender_bytes"`
	InboxMessages  int `json:"inbox_messages"`
	InboxBytes     int `json:"inbox_bytes"`
}

type SenderUsage struct {
	AgentID        string `json:"agent_id"`
	Unacknowledged int    `json:"unacknowledged"`
	Unreceived     int    `json:"unreceived"`
	Bytes          int    `json:"bytes"`
}

type RetainedUsage struct {
	Messages      int `json:"messages"`
	Bytes         int `json:"bytes"`
	LimitMessages int `json:"limit_messages"`
	LimitBytes    int `json:"limit_bytes"`
}

// PageRequest selects unacknowledged operator-inbox messages with
// AfterSeq < seq <= UpperSeq. UpperSeq 0 starts a new scan bounded by the
// highest committed message.
type PageRequest struct {
	AfterSeq  uint64 `json:"after_seq"`
	UpperSeq  uint64 `json:"upper_seq"`
	Limit     int    `json:"limit,omitempty"`      // omitted or 0 means OperatorPageDefault (history: HistoryDefault)
	BeforeSeq uint64 `json:"before_seq,omitempty"` // operator_history: exclusive; 0 means no cut
}

// OperatorPage is a live pending view: acknowledgments can remove rows from a scan.
type OperatorPage struct {
	Messages     []Message `json:"messages"`
	NextAfterSeq uint64    `json:"next_after_seq"`
	UpperSeq     uint64    `json:"upper_seq"`
	More         bool      `json:"more"`
}
