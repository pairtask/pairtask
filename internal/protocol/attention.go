package protocol

// Attention (#422): do-not-disturb for one local agent, and an operator's
// urgent message that still interrupts. Messages are always stored and
// delivered; DND only stops the delivered frame from interrupting the
// agent. The broker decides for each recipient and stamps the frame; a
// sender never sets it.

// AttentionVersion: msg frames carry attention; operator_attention sets
// and reads an agent's DND; operator_send takes urgent; a summary of the
// messages kept quiet is sent when DND ends, and the adapter confirms it
// submitted that summary.
const AttentionVersion = 32

const (
	TypeOperatorAttention        = "operator_attention"         // operator: agent_id and attention_op
	TypeOperatorAttentionResult  = "operator_attention_result"  // its answer: attention_state
	TypeAttentionSummary         = "attention_summary"          // broker -> agent: what stayed quiet during DND
	TypeAttentionSubmitted       = "attention_submitted"        // agent -> broker: the host was shown that summary
	TypeAttentionSubmittedResult = "attention_submitted_result" // its answer: accepted
)

// Attention operations.
const (
	AttentionOn     = "on"
	AttentionOff    = "off"
	AttentionStatus = "status"
)

// Dispositions the broker stamps on a delivered msg; none means notify.
const (
	AttentionSuppressed = "suppressed" // DND: keep it, don't interrupt
	AttentionUrgent     = "urgent"     // the operator's urgent message, through DND
)

// What a local sender is told of a recipient whose DND is on, beside
// AttentionSuppressed: its adapter can't keep quiet, or it isn't connected.
const (
	AttentionUnsupported = "unsupported"
	AttentionOffline     = "offline"
)

// Why a DND that is on isn't enforced now.
const (
	AttentionReasonOldAdapter = "adapter_too_old"
	AttentionReasonOffline    = "offline"
)

// AttentionState is one agent's DND as the operator reads it. Pending
// counts messages kept quiet and not yet acknowledged; InboxUsed every
// unacknowledged message, quiet or not.
type AttentionState struct {
	AgentID    string `json:"agent_id"`
	DND        bool   `json:"dnd"`
	Since      string `json:"since,omitempty"`
	Enforced   bool   `json:"enforced"`
	Reason     string `json:"reason,omitempty"`
	Pending    int    `json:"pending"`
	InboxUsed  int    `json:"inbox_used"`
	InboxLimit int    `json:"inbox_limit"`
}

// AttentionSummary names the messages kept quiet during DND that are still
// unacknowledged, oldest first, once DND ends.
type AttentionSummary struct {
	Generation uint64   `json:"generation"`
	Count      int      `json:"count"`
	MessageIDs []string `json:"message_ids"`
}
