package protocol

import "time"

// TypeOperatorAgentReads asks the broker how each of its agents is keeping
// up with its durable inbox; the answer carries AgentReads. It reads
// nothing for anyone and marks nothing.
const TypeOperatorAgentReads = "operator_agent_reads"

// AgentRead is one agent's reading: whether a session is connected, how
// many durable messages wait unacknowledged, the oldest one's time, and
// when it last acknowledged one.
type AgentRead struct {
	AgentID        string     `json:"agent_id"`
	Online         bool       `json:"online"`
	Unread         int        `json:"unread"`
	OldestUnreadAt *time.Time `json:"oldest_unread_at,omitempty"`
	LastReadAt     *time.Time `json:"last_read_at,omitempty"`
}
