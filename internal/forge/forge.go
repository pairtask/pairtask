// Package forge is what a reviewer's side needs from a code forge (#201):
// pin a change's exact revisions, list what it changed, publish a review
// and find one already published. It runs beside the reviewing agent, with
// that agent's own access; the broker never talks to a forge.
package forge

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Target and Revisions are the broker's own.
type (
	Target    = protocol.ReviewTarget
	Revisions = protocol.Revisions
)

// Events a review is published as: forge-neutral, the verdict outcomes.
const (
	EventApprove        = protocol.OutcomeApprove
	EventRequestChanges = protocol.OutcomeChangesRequested
	EventComment        = protocol.OutcomeComment
)

// LineRange is new-side lines [From, To] a finding may name.
type LineRange struct{ From, To int }

// FileChange is one changed file and the lines a finding may name in it.
type FileChange struct {
	Path  string
	Lines []LineRange
}

// Caps are what an adapter can do. Nothing is published as a formal event
// the adapter can't make.
type Caps struct {
	FormalApprove        bool
	FormalRequestChanges bool
	LineComments         bool
	Reconcile            bool
}

// Submission is one review to publish.
type Submission struct {
	Target   Target
	Pinned   Revisions // the head it is published on
	Event    string    // EventApprove, EventRequestChanges or EventComment
	Summary  string
	Findings []protocol.Finding
	Marker   string // embedded in the published body; see Marker
	Identity string // the account pinned with the write intent
}

// Receipt is a published review as the forge records it, with the event it
// was recorded as.
type Receipt struct {
	protocol.PublicationReceipt
	Event string
}

// Expected is what a review this side published must match: its marker,
// account, head and event. Anything else, even with the marker, is not ours.
type Expected struct {
	Marker, Identity, Revision, Event string
}

// Matches reports a receipt that is exactly the expected review: the
// account, the head, the event, and a formal flag that fits the event.
func Matches(r Receipt, e Expected) bool {
	if r.Identity != e.Identity || r.Revision != e.Revision {
		return false
	}
	return r.Event == e.Event && r.Formal == (e.Event != EventComment)
}

// Forge is one code forge, as the reviewing agent's account sees it.
type Forge interface {
	Name() string
	Capabilities() Caps
	// Identity is the account this adapter publishes as.
	Identity(ctx context.Context) (string, error)
	// Resolve is the change's current base and head, as full commit IDs.
	Resolve(ctx context.Context, t Target) (Revisions, error)
	// Changes lists what the change modified between the pinned revisions.
	Changes(ctx context.Context, t Target, r Revisions) ([]FileChange, error)
	// Publish writes one review. An Error of class uncertain means the
	// request may have reached the forge.
	Publish(ctx context.Context, s Submission) (Receipt, error)
	// Find looks for a review matching e exactly; one that matches only in
	// part is reported as ErrAmbiguous, never as found.
	Find(ctx context.Context, t Target, e Expected) (Receipt, bool, error)
}

// Error classes; the broker records the class, never the forge's own text.
const (
	ClassNotFound    = "not_found"
	ClassForbidden   = "forbidden"
	ClassSelfReview  = "self_review"
	ClassRateLimited = "rate_limited"
	ClassUnavailable = "unavailable"
	ClassUnsupported = "unsupported"
	ClassUncertain   = "uncertain" // a write may have landed
	ClassAmbiguous   = "ambiguous" // a review carries our marker but not all of our facts
	ClassInvalid     = "invalid"   // the forge refused what was sent
)

// Error is a forge failure of one class.
type Error struct {
	Class string
	Err   error
}

func (e *Error) Error() string { return e.Class + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Errorf is an Error of class.
func Errorf(class, format string, args ...any) error {
	return &Error{Class: class, Err: fmt.Errorf(format, args...)}
}

// ClassOf is err's class: unavailable for anything that isn't an Error.
func ClassOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Class
	}
	return ClassUnavailable
}

// Registry is the adapters this side has, by forge name.
type Registry map[string]Forge

// Lookup is the adapter for name, or an unsupported Error.
func (r Registry) Lookup(name string) (Forge, error) {
	if f, ok := r[name]; ok {
		return f, nil
	}
	return nil, Errorf(ClassUnsupported, "no adapter for forge %q on this side", name)
}

// Marker tags a published review with its review, attempt and write
// intent. The review ID is base64url-encoded, so no request ID can break
// out of the comment.
func Marker(reviewID string, attempt, intent int) string {
	id := base64.RawURLEncoding.EncodeToString([]byte(reviewID))
	return "<!-- pairtask-review:" + id + ":" + strconv.Itoa(attempt) + ":" + strconv.Itoa(intent) + " -->"
}
