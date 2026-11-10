package protocol

import (
	"errors"
	"time"
	"unicode"
)

// Decision requests (#352, Slack-only first): an agent that needs the
// operator's decision asks the broker to record it and to tell the operator
// outside the chat. The request hangs on an anchor, a durable message from
// that agent to the operator in the chat; the operator's reply to the
// anchor answers it. Delivery of the notice never counts as a decision.

const (
	// TypeDecisionRequest is agent → broker: record and notify.
	TypeDecisionRequest = "decision_request"
	// TypeDecisionResult is broker → agent: the record as it stands.
	TypeDecisionResult = "decision_result"
	// DecisionVersion gates the frames.
	DecisionVersion = 26
)

// Decision limits.
const (
	DecisionSummaryMax = 200 // characters
	DecisionKeyMax     = 128 // bytes
)

// Decision states and delivery outcomes.
const (
	DecisionOpen     = "open"
	DecisionAnswered = "answered"

	DeliveryPending = "pending"
	DeliverySent    = "sent"
	DeliveryFailed  = "failed"
	DeliveryOff     = "off" // no notifier configured: recorded, nobody told
)

// Error codes.
const (
	ErrDecisionAnchor = "decision_invalid_anchor" // not a durable message from this agent to the operator
)

// Decision is one recorded request.
type Decision struct {
	ID         string     `json:"id"`
	Agent      string     `json:"agent"`
	Key        string     `json:"key,omitempty"`
	Anchor     string     `json:"anchor"` // the chat message the operator answers
	Summary    string     `json:"summary"`
	State      string     `json:"state"`
	Delivery   string     `json:"delivery"`
	Reason     string     `json:"reason,omitempty"` // why delivery is pending or failed, a class
	Replayed   bool       `json:"replayed,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	AnsweredAt *time.Time `json:"answered_at,omitempty"`
}

// ValidateDecisionRequest checks a request's summary and key.
func ValidateDecisionRequest(summary, key string) error {
	runes := []rune(summary)
	if len(runes) == 0 || len(runes) > DecisionSummaryMax {
		return errors.New("summary is required, at most 200 characters")
	}
	for _, r := range runes {
		if !unicode.IsPrint(r) {
			return errors.New("summary must be printable text on one line")
		}
	}
	if len(key) > DecisionKeyMax {
		return errors.New("key is at most 128 bytes")
	}
	for _, r := range key {
		if !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return errors.New("key must be printable without spaces")
		}
	}
	return nil
}
