package protocol

import "time"

// Live operator session limits.
const (
	SessionEventQueue   = 256
	SessionInFlight     = 8
	SessionControlSlots = 2
	SnapshotAgents      = StatusLimit
	SnapshotRecentSent  = 16
	StagesDefault       = 64
	StagesMax           = 256
)

// Session event kinds.
const (
	EventConversationChanged = "conversation_changed"
	EventInboxChanged        = "inbox_changed"
	EventStageChanged        = "stage_changed"
	EventPresenceChanged     = "presence_changed"
	EventResyncRequired      = "resync_required"
	EventSendRefused         = "send_refused" // an agent's send to the operator hit an inbox bound
)

// SessionCapabilities are advertised in the operator_session response.
var SessionCapabilities = []string{"send", "receive", "received", "ack", "snapshot", "message_stages", "events", "history", CapabilityMentions, CapabilityPairing, CapabilityPairingRedeem, CapabilityPairingContacts, CapabilityPairingContactSummary, CapabilityReviews, CapabilityContactMessages, CapabilityContactRouting, CapabilityContactReviews, CapabilityContactNames}

// CapabilityMentions: operator_send to a room or group may carry mentions
// (#164); a broker without it would drop them, so the chat refuses such a
// send instead.
const CapabilityMentions = "mentions"

// Watermark orders session state: Revision increases by one per event within
// an Epoch, which is new on every broker start.
type Watermark struct {
	Epoch    string `json:"epoch"`
	Revision uint64 `json:"revision"`
}

type SessionInfo struct {
	SessionID    string    `json:"session_id"`
	Epoch        string    `json:"epoch,omitempty"`
	Capabilities []string  `json:"capabilities,omitempty"`
	OpenedAt     time.Time `json:"opened_at"`
}

// HasCapability reports whether the operator session advertises a feature.
func (s SessionInfo) HasCapability(name string) bool {
	for _, c := range s.Capabilities {
		if c == name {
			return true
		}
	}
	return false
}

// RecipientStage is a current delivery state; it can move back to accepted
// when a reconnect moves the delivery to a new session.
type RecipientStage struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
	Stage     string `json:"stage"`
}

// SentSummary describes an operator-sent message; it never carries the payload.
type SentSummary struct {
	MessageID         string           `json:"message_id"`
	Seq               uint64           `json:"seq"`
	TS                time.Time        `json:"ts"`
	To                string           `json:"to"`
	Room              string           `json:"room,omitempty"`     // v9: a "*" to a room
	Mentions          []string         `json:"mentions,omitempty"` // the members it addressed (#164)
	Recipients        []RecipientStage `json:"recipients"`
	ReceiptsTruncated bool             `json:"receipts_truncated"`
}

type AgentPresence struct {
	AgentID         string `json:"agent_id"`
	SessionID       string `json:"session_id"`
	ProtocolVersion int    `json:"protocol_version"`
}

// OperatorSnapshot is a bounded view; omitted entities are unknown, not absent.
type OperatorSnapshot struct {
	Watermark           Watermark       `json:"watermark"`
	Agents              []AgentPresence `json:"agents"`
	Rooms               []RoomInfo      `json:"rooms"`           // v9: rooms and members, bounded (#153)
	RoomsTotal          int             `json:"rooms_total"`     // every room
	RoomsTruncated      bool            `json:"rooms_truncated"` // the list or a frame trim left rooms out
	AgentsTotal         int             `json:"agents_total"`
	AgentsTruncated     bool            `json:"agents_truncated"`
	InboxHeadSeq        uint64          `json:"inbox_head_seq"`
	InboxPending        int             `json:"inbox_pending"`
	RecentSent          []SentSummary   `json:"recent_sent"`
	RecentSentTruncated bool            `json:"recent_sent_truncated"`
	// Agent statuses: connected agents first, then retained offline ones.
	AgentStatus          []AgentStatus `json:"agent_status"`
	AgentStatusTotal     int           `json:"agent_status_total"`
	AgentStatusTruncated bool          `json:"agent_status_truncated"`
	Claims               []ClaimSet    `json:"claims"`       // newest operator messages with claims (#141)
	ClaimsTotal          int           `json:"claims_total"` // operator messages with claims
	ClaimsTruncated      bool          `json:"claims_truncated"`
}

type StagePage struct {
	Watermark      Watermark        `json:"watermark"`
	MessageID      string           `json:"message_id"`
	Recipients     []RecipientStage `json:"recipients"`
	NextAfterAgent string           `json:"next_after_agent"`
	More           bool             `json:"more"`
}

// SessionEvent is a notification, never a payload.
type SessionEvent struct {
	Kind              string    `json:"kind"`
	Watermark         Watermark `json:"watermark"`
	Seq               uint64    `json:"seq,omitempty"`
	MessageID         string    `json:"message_id,omitempty"`
	Direction         string    `json:"direction,omitempty"`
	HeadSeq           uint64    `json:"head_seq,omitempty"`
	Pending           *int      `json:"pending,omitempty"`
	AgentID           string    `json:"agent_id,omitempty"`
	SessionID         string    `json:"session_id,omitempty"`
	Stage             string    `json:"stage,omitempty"`
	State             string    `json:"state,omitempty"`
	ProtocolVersion   int       `json:"protocol_version,omitempty"`
	Detail            string    `json:"detail,omitempty"`              // send_refused: the bound that refused
	OperatorMessageID string    `json:"operator_message_id,omitempty"` // claims_changed
	ReviewID          string    `json:"review_id,omitempty"`           // reviews_changed (#201)
	ContactKey        string    `json:"contact_key,omitempty"`         // contact_messages_changed (#210)
}

// Operator history limits. New operator payloads are capped so every new
// history entry fits a page with its envelope.
const (
	OperatorPayloadMax = 512 << 10
	HistoryDefault     = 16
	HistoryMax         = 32
	HistoryStages      = 16
)

// HistoryPage is a newest-first page of the operator's conversation.
type HistoryPage struct {
	Watermark     Watermark      `json:"watermark"`
	UpperSeq      uint64         `json:"upper_seq"`
	NextBeforeSeq uint64         `json:"next_before_seq"`
	More          bool           `json:"more"`
	Entries       []HistoryEntry `json:"entries"`
}

// HistoryEntry carries a message sent by or to the operator. When BodyOmitted
// is set, the message has no payload field; its text isn't available here.
type HistoryEntry struct {
	Message           Message          `json:"message"`
	Direction         string           `json:"direction"`
	BodyOmitted       bool             `json:"body_omitted,omitempty"`
	SenderSession     string           `json:"sender_session,omitempty"`
	Recipients        []RecipientStage `json:"recipients,omitempty"`
	ReceiptsTruncated bool             `json:"receipts_truncated,omitempty"`
	OperatorStage     string           `json:"operator_stage,omitempty"`
}
