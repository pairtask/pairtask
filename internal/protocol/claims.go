package protocol

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Claim frames (#141, v8): an agent records that it owns, or reviews, one
// task of an operator request, so several agents addressed together don't do
// the same work. A claim coordinates; it is never an approval, an
// authorization or a file lock. request_id only correlates the answer.
const (
	TypeClaimSet     = "claim_set"     // agent → broker: claim a task; answered by claim_result or error
	TypeClaimRelease = "claim_release" // agent → broker: the holder frees its claim
	TypeClaimDone    = "claim_done"    // agent → broker: the owner finishes its task; terminal
	TypeClaimGet     = "claim_get"     // agent → broker: read the claims of one operator message
	TypeClaimResult  = "claim_result"  // broker → agent: the claims of that operator message

	EventClaimsChanged = "claims_changed" // operator session: claims of operator_message_id changed

	ErrInvalidClaim     = "invalid_claim"
	ErrClaimConflict    = "claim_conflict" // carries claims naming the holder
	ErrClaimRateLimited = "claim_rate_limited"
)

// Claim roles and states.
const (
	RoleOwner    = "owner"
	RoleReviewer = "reviewer"

	ClaimActive   = "active"
	ClaimReleased = "released"
	ClaimDone     = "done"
)

// Claim bounds: per operator message, and the operator snapshot's messages.
const (
	ClaimsPerMessage = 16
	SnapshotClaimed  = 32 // newest operator messages with claims in a snapshot
	ClaimTopicBytes  = 120
)

// TaskReply is the canonical key every agent claims before answering an
// operator message sent to everyone.
const TaskReply = "reply"

var taskKey = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// Claim is one recorded claim. Identity and times are set by the broker.
type Claim struct {
	OperatorMessageID string     `json:"operator_message_id"`
	Task              string     `json:"task"`
	Topic             string     `json:"topic"`
	Role              string     `json:"role"`
	AgentID           string     `json:"agent_id"`
	State             string     `json:"state"`
	ClaimedAt         time.Time  `json:"claimed_at"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"` // released or done
}

// ClaimPage is a bounded list of claims with the total. Embedded in a frame,
// all three fields are present whenever the page is, even when empty.
type ClaimPage struct {
	Claims    []Claim `json:"claims"`
	Total     int     `json:"claims_total"`
	Truncated bool    `json:"claims_truncated"`
	// Assignments (#278), with their own bound, only for a caller at
	// AssignVersion, which always gets all three fields: an older one sees
	// claims exactly as before.
	*AssignmentPage
}

// AssignmentPage is a message's assignments with their total.
type AssignmentPage struct {
	Assignments          []Assignment `json:"assignments"`
	AssignmentsTotal     int          `json:"assignments_total"`
	AssignmentsTruncated bool         `json:"assignments_truncated"`
}

// ClaimSet is the claims of one operator message with their bound.
type ClaimSet struct {
	OperatorMessageID string `json:"operator_message_id"`
	ClaimPage
}

// ValidateClaimMessage checks the operator message a claim frame names.
func ValidateClaimMessage(messageID string) error {
	if messageID == "" || len(messageID) > 128 {
		return errors.New("operator_message_id is required, at most 128 bytes")
	}
	return nil
}

// ValidateTask checks a stable task key.
func ValidateTask(task string) error {
	if !taskKey.MatchString(task) {
		return errors.New("task must be 1 to 64 of a-z, 0-9 and -")
	}
	return nil
}

// ValidateRole checks a claim role.
func ValidateRole(role string) error {
	if role != RoleOwner && role != RoleReviewer {
		return fmt.Errorf("role must be %s or %s", RoleOwner, RoleReviewer)
	}
	return nil
}

// ValidateTopic checks a claim's display label.
func ValidateTopic(topic string) error {
	if len(topic) > ClaimTopicBytes {
		return fmt.Errorf("topic: at most %d bytes", ClaimTopicBytes)
	}
	if err := statusText(topic); err != nil {
		return fmt.Errorf("topic: %w", err)
	}
	return nil
}
