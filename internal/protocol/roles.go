package protocol

import (
	"errors"
	"time"
	"unicode"
)

// Agent roles (#339, calibration): each agent declares its own one-line
// role; the shadow classifier shows it to the model, and the agent reads
// how the model's picks compared with the replies it actually claimed, to
// sharpen the line. Nothing here routes, assigns or claims.

const (
	// RoleVersion gates the frames.
	RoleVersion = 28
	// TypeRoleSet is agent → broker: set or clear the sender's own role.
	TypeRoleSet = "role_set"
	// TypeRoleResult is broker → agent: the role as stored.
	TypeRoleResult = "role_result"
	// TypeClassifierFeedbackGet is agent → broker: the sender's own feedback.
	TypeClassifierFeedbackGet = "classifier_feedback_get"
	// TypeClassifierFeedbackResult is broker → agent: that feedback.
	TypeClassifierFeedbackResult = "classifier_feedback_result"
)

// Role limits.
const (
	AgentRoleMax      = 160 // characters
	FeedbackListed    = 20  // items by default
	FeedbackListedMax = 50
	FeedbackWindow    = 200 // newest predictions where the agent was eligible
)

// Feedback verdicts, from the asking agent's point of view.
const (
	VerdictAgreed        = "agreed"         // the model picked you and you claimed the reply
	VerdictMissed        = "missed"         // you claimed it; the model abstained or picked another
	VerdictWrongPick     = "wrong_pick"     // the model picked you; another agent claimed it
	VerdictUnclaimedPick = "unclaimed_pick" // the model picked you; nobody has claimed it
)

// AgentRole is one agent's declared role.
type AgentRole struct {
	AgentID   string    `json:"agent_id"`
	Role      string    `json:"role"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ClassifierFeedback is one agent's view of the shadow classifier.
type ClassifierFeedback struct {
	AgentID string         `json:"agent_id"`
	Role    string         `json:"role"`    // as the model now sees it; "" when none was declared
	Enabled bool           `json:"enabled"` // the classifier runs on this broker
	Counts  map[string]int `json:"counts"`  // by verdict, over the window
	Items   []FeedbackItem `json:"items"`   // newest first
}

// FeedbackItem is one prediction that concerns the agent. No message text:
// the agent received the message and can look it up by its ID.
type FeedbackItem struct {
	MessageID       string    `json:"message_id"`
	At              time.Time `json:"at"`
	Outcome         string    `json:"outcome"`
	Predicted       string    `json:"predicted,omitempty"`
	Confidence      float64   `json:"confidence"`
	YourProbability float64   `json:"your_probability"`
	Claimant        string    `json:"claimant,omitempty"`
	Verdict         string    `json:"verdict"`
}

// ValidateAgentRole checks a role line; "" clears.
func ValidateAgentRole(role string) error {
	runes := []rune(role)
	if len(runes) > AgentRoleMax {
		return errors.New("role is at most 160 characters")
	}
	for _, r := range runes {
		if !unicode.IsPrint(r) {
			return errors.New("role must be printable text on one line")
		}
	}
	return nil
}

// FeedbackLimit is the number of items to list for a requested limit.
func FeedbackLimit(requested int) int {
	switch {
	case requested <= 0:
		return FeedbackListed
	case requested > FeedbackListedMax:
		return FeedbackListedMax
	}
	return requested
}
