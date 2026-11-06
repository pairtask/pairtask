package protocol

import (
	"errors"
	"time"
)

// Pending consent (#293): a paired workspace's request for something this
// side never decided is held for the operator, who allows it once, for this
// session, always, declines it, or decides later.

// ConsentVersion is the operator protocol that carries consent prompts.
const ConsentVersion = 24

// CapabilityConsentPrompts: the broker holds undecided requests for the
// operator's decision.
const CapabilityConsentPrompts = "consent_prompts"

// Consent operations of operator_contact.
const (
	ContactOpConsentList   = "consent_list"
	ContactOpConsentDecide = "consent_decide"
	ContactOpConsentOpt    = "consent_opt"
	ContactOpConsentReset  = "consent_reset"
)

// Consent choices.
const (
	ConsentOnce    = "once"
	ConsentSession = "session"
	ConsentAlways  = "always"
	ConsentDecline = "decline"
	ConsentLater   = "later"
)

// Consent operations a target may be asked for.
const (
	ConsentMessage = "message"
	ConsentReview  = "review"
	ConsentPublish = "publish"
)

// EventConsentChanged: a held request arrived, was decided or expired.
const EventConsentChanged = "consent_changed"

// EventConsentConflict: a paired workspace sent an outcome contradicting the
// one this side holds; it was refused (not stored).
const EventConsentConflict = "consent_conflict"

// ConsentTarget is one local agent a held request names, and what it asks
// of it.
type ConsentTarget struct {
	Agent      string   `json:"agent"`
	Operations []string `json:"operations"`
}

// ConsentView is one held request as the operator decides it.
type ConsentView struct {
	ID           string          `json:"id"`
	Cursor       int64           `json:"cursor"` // pass as contact_consent_before for older ones
	Contact      string          `json:"contact"`
	WorkspaceKey string          `json:"workspace_key"`
	Label        string          `json:"label,omitempty"`
	ClaimedFrom  string          `json:"claimed_from,omitempty"`
	Targets      []ConsentTarget `json:"targets"`
	Review       *ReviewAsk      `json:"review,omitempty"`
	Text         string          `json:"text,omitempty"`
	ReceivedAt   time.Time       `json:"received_at"`
	ExpiresAt    time.Time       `json:"expires_at"`
	Digest       string          `json:"digest"`
	Revision     int64           `json:"revision"`
	State        string          `json:"state"`
}

// ConsentPage is a page of held requests, newest first.
type ConsentPage struct {
	Items []ConsentView `json:"items"`
	Total int           `json:"total"`
	More  bool          `json:"more,omitempty"`
	Next  int64         `json:"next,omitempty"` // cursor for the next page when More
}

// ConsentDecideRequest is the operator's decision on one held request, as
// shown: epoch and gen are stale guards, never authority.
type ConsentDecideRequest struct {
	ConsentID string `json:"consent_id"`
	Digest    string `json:"digest"`
	Revision  int64  `json:"revision"`
	Choice    string `json:"choice"`
	Epoch     string `json:"epoch"`
	Gen       uint64 `json:"gen"`
}

// ConsentResult is what a decision did, or why it didn't.
type ConsentResult struct {
	ConsentID string `json:"consent_id"`
	State     string `json:"state"`
	Outcome   string `json:"outcome,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Consent result reasons.
const (
	ConsentStale        = "stale"
	ConsentExpired      = "expired"
	ConsentRevoked      = "revoked"
	ConsentPaused       = "paused"
	ConsentSessionEnded = "session_ended"
	ConsentDenied       = "denied"
	ConsentFull         = "full"
	ConsentRate         = "rate"
)

// consentOperations are the operations a scope reset may name.
var consentOperations = map[string]bool{ConsentMessage: true, ConsentReview: true, ConsentPublish: true}

// consentChoices are the choices a decision may make.
var consentChoices = map[string]bool{ConsentOnce: true, ConsentSession: true, ConsentAlways: true, ConsentDecline: true, ConsentLater: true}

// validKeyFor requires a contact key, unless the operation spans every
// contact without one.
func validKeyFor(msg Message, allowed contactFieldSet) error {
	if allowed.anyKey && msg.ContactKey == "" {
		return nil
	}
	return validContactKey(msg.ContactKey)
}

// validConsent checks what a consent operation carries.
func validConsent(msg Message, allowed contactFieldSet) error {
	switch {
	case msg.ContactConsentBefore < 0:
		return errors.New("contact_consent_before is a cursor, 0 or more")
	case allowed.decide:
		return validConsentDecide(msg.ConsentDecide)
	case allowed.consentOn && msg.ConsentOn == nil:
		return errors.New("consent_opt needs consent_on")
	case allowed.scope:
		return validConsentScope(msg)
	}
	return nil
}

// validConsentDecide: a decision names a held request as shown, and one
// known choice.
func validConsentDecide(d *ConsentDecideRequest) error {
	switch {
	case d == nil:
		return errors.New("consent_decide needs consent_decide")
	case !shownRequest(d):
		return errors.New("consent_decide needs the consent_id and digest it was shown")
	case d.Revision < 0:
		return errors.New("consent_decide revision is 0 or more")
	case !consentChoices[d.Choice]:
		return errors.New("consent_decide choice is once, session, always, decline or later")
	}
	return nil
}

// shownRequest: the decision names a request ID and digest as shown.
func shownRequest(d *ConsentDecideRequest) bool {
	if !validMessageIDText(d.ConsentID) {
		return false
	}
	return d.Digest != "" && len(d.Digest) <= ContactIDBytes
}

// validConsentScope: a reset names one agent and one operation.
func validConsentScope(msg Message) error {
	if ValidateAgentID(msg.ConsentAgent) != nil || !consentOperations[msg.ConsentOperation] {
		return errors.New("consent_reset needs consent_agent and consent_operation: message, review or publish")
	}
	return nil
}
