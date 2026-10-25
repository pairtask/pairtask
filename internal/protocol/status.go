package protocol

import "time"

const (
	TypeStatus          = "status"
	StatusSchemaVersion = 2
	StatusLimit         = 100
)

// StatusSnapshot contains observations, never message bodies or model activity.
// Nullable counts/timestamps mean unavailable, not zero or a guessed outcome.
type StatusSnapshot struct {
	SchemaVersion        int             `json:"schema_version"`
	Reachable            bool            `json:"reachable"`
	Health               string          `json:"health"`
	SnapshotAt           *time.Time      `json:"snapshot_at"`
	BrokerStartedAt      *time.Time      `json:"broker_started_at"`
	ProtocolVersion      int             `json:"protocol_version"`
	ConnectedSessions    *int            `json:"connected_sessions"`
	Sessions             []SessionStatus `json:"sessions"`
	SessionLimit         int             `json:"session_limit"`
	SessionsTruncated    bool            `json:"sessions_truncated"`
	HistoryAvailable     bool            `json:"history_available"`
	DurablePending       *PendingStatus  `json:"durable_pending"`
	LegacyUnacknowledged *int            `json:"legacy_unacknowledged"`
	// OperatorPending counts the same rows as DurablePending for the operator
	// inbox; nil from an older broker means unknown, not zero.
	OperatorPending *int `json:"operator_pending,omitempty"`
	// AgentStatus is the agents' latest declarations (schema 2), bounded like
	// the operator snapshot's. Unavailable means unknown, never "none".
	AgentStatus          []AgentStatus `json:"agent_status"`
	AgentStatusTotal     int           `json:"agent_status_total"`
	AgentStatusTruncated bool          `json:"agent_status_truncated"`
	AgentStatusAvailable bool          `json:"agent_status_available"`
	Error                string        `json:"error,omitempty"`
}

type SessionStatus struct {
	AgentID          string     `json:"agent_id"`
	SessionID        string     `json:"session_id"`
	State            string     `json:"state"`
	ConnectedAt      time.Time  `json:"connected_at"`
	DisconnectedAt   *time.Time `json:"disconnected_at"`
	LastSeenAt       *time.Time `json:"last_seen_at"`
	DisconnectReason *string    `json:"disconnect_reason"`
}

// Total counts pending recipient deliveries, so a broadcast can contribute more
// than one. Only committed agent acknowledgment removes a durable delivery.
type PendingStatus struct {
	Total               int                `json:"total"`
	Recipients          []PendingRecipient `json:"recipients"`
	RecipientLimit      int                `json:"recipient_limit"`
	RecipientsTruncated bool               `json:"recipients_truncated"`
}

type PendingRecipient struct {
	AgentID string `json:"agent_id"`
	Pending int    `json:"pending"`
}

func UnavailableStatus() StatusSnapshot {
	return StatusSnapshot{SchemaVersion: StatusSchemaVersion, Health: "unavailable",
		Sessions: []SessionStatus{}, SessionLimit: StatusLimit, AgentStatus: []AgentStatus{}}
}
