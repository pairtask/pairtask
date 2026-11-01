package protocol

import "time"

// Tags that assign (#278): an operator message to everyone or a room that
// tags agents assigns each of them its reply. An assignment is the broker's
// record, apart from claims: a tagged agent holds the message's reply as its
// own, released or finished through the claim frames. Groups keep tags as
// attention only, and nothing but the local operator's own send assigns.

const (
	// CapabilityAssignments: the broker serves tags that assign, so a tag in
	// All stays public instead of becoming a private send.
	CapabilityAssignments = "assignments"

	AssignmentAssigned = "assigned"
	AssignmentReleased = "released"
	AssignmentDone     = "done"

	// ErrInvalidReplyAudience refuses an agent's reply to an assigned public
	// message anywhere but where it was asked.
	ErrInvalidReplyAudience = "invalid_reply_audience"
)

// Assignment is one tagged agent's assignment of an operator message's reply.
type Assignment struct {
	OperatorMessageID string     `json:"operator_message_id"`
	AgentID           string     `json:"agent_id"`
	Task              string     `json:"task"` // always TaskReply
	State             string     `json:"state"`
	AssignedAt        time.Time  `json:"assigned_at"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"` // released or done
}

// AssignmentView is what an operator frame tells the agent it assigns.
type AssignmentView struct {
	Task  string `json:"task"`
	State string `json:"state"`
}
