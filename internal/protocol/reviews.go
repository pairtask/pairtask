package protocol

import "time"

// Review requests (#201, v10): the operator asks one agent to review one
// change, pinned to its exact base and head revisions. The broker holds the
// request, its lifecycle and the verdict; it has no network, so the
// reviewer's own side resolves revisions and publishes to the forge, and
// reports what happened. A verdict, and an approval published from it, is
// evidence for a forge's own merge gate; it never merges anything.
const (
	TypeReviewGet         = "review_get"         // agent → broker: its reviews, or one by review_id
	TypeReviewPrepare     = "review_prepare"     // agent → broker: pin the revisions it resolved; requested → ready
	TypeReviewAccept      = "review_accept"      // agent → broker: start reviewing a ready request; ready → in_review, attempt 1
	TypeReviewDecline     = "review_decline"     // agent → broker: decline a requested or ready review
	TypeReviewRestart     = "review_restart"     // agent → broker: a new attempt replaces the current one; same revisions
	TypeReviewSubmit      = "review_submit"      // agent → broker: the verdict of the current attempt; in_review → completed
	TypeReviewObserve     = "review_observe"     // agent → broker: the change as seen now; marks a verdict stale, never touches its publication
	TypeReviewPublication = "review_publication" // agent → broker: a publication step, compare-and-set on its current state
	TypeReviewResult      = "review_result"      // broker → agent: the review(s) concerned

	TypeOperatorReview       = "operator_review"        // operator → broker: review_op request, cancel, list or status
	TypeOperatorReviewResult = "operator_review_result" // broker → operator

	EventReviewsChanged = "reviews_changed" // operator session: a review request changed

	ErrInvalidReview     = "invalid_review"
	ErrReviewConflict    = "review_conflict" // a state, attempt or revision that no longer holds
	ErrReviewRateLimited = "review_rate_limited"
)

// ReviewVersion is the protocol version that carries review requests.
const ReviewVersion = 10

// Operator review operations.
const (
	ReviewOpRequest = "request" // message_id is the request ID: an identical retry returns the same review
	ReviewOpCancel  = "cancel"
	ReviewOpList    = "list"
	ReviewOpStatus  = "status"
)

// Review states. requested and ready wait for the reviewer; in_review runs
// one attempt; the rest are final.
const (
	ReviewRequested = "requested"
	ReviewReady     = "ready" // revisions pinned, no review work dispatched yet
	ReviewInReview  = "in_review"
	ReviewCompleted = "completed"
	ReviewDeclined  = "declined"
	ReviewCancelled = "cancelled"
	ReviewExpired   = "expired"
)

// Verdict outcomes, forge-neutral; an adapter maps them to its own events.
const (
	OutcomeApprove          = "approve"
	OutcomeChangesRequested = "changes_requested"
	OutcomeComment          = "comment"
)

// Publication states, kept apart from the review's own state.
// none: the policy publishes nothing. pending: a verdict waits to be
// published. submitting: one bridge holds the intent to write. published,
// failed: the forge's answer is known. uncertain: a write may have landed;
// only reconciliation moves it on. stale: the change moved before
// publishing (or before a retry), so nothing more is published from this
// verdict. A published receipt is kept when the change moves later; the
// review's own stale field records that.
//
// Steps: pending → submitting | stale; submitting → published | failed |
// uncertain; failed → submitting | stale; uncertain → published.
const (
	PublicationNone       = "none"
	PublicationPending    = "pending"
	PublicationSubmitting = "submitting"
	PublicationPublished  = "published"
	PublicationFailed     = "failed"
	PublicationUncertain  = "uncertain"
	PublicationStale      = "stale"
	PublicationCancelled  = "cancelled" // the review was cancelled first: nothing more is published from this verdict
)

// Review bounds.
const (
	ReviewsOpen           = 64 // open (not final) requests in all
	ReviewsOpenPerAgent   = 8
	ReviewAttempts        = 4 // attempts per review: the first and up to three restarts
	ReviewWriteIntents    = 4 // write intents per verdict: the first and up to three retries
	ReviewScopeBytes      = 1024
	ReviewSummaryBytes    = 4096
	ReviewFindings        = 100
	ReviewFindingBytes    = 1024 // a finding's summary
	ReviewPathBytes       = 512
	ReviewVerdictBytes    = 256 << 10 // a verdict as encoded JSON, so a review always fits one frame
	ReviewReasonBytes     = 256
	ReviewListed          = 64 // newest reviews in a list
	ReviewUnaccepted      = 24 * time.Hour
	ReviewUnfinished      = 72 * time.Hour
	ReviewRevisionBytes   = 64  // a full commit ID: 40 (SHA-1) or 64 (SHA-256) hex digits
	ReviewRepositoryBytes = 256 // host/owner/name
	ReviewChangeBytes     = 64  // the forge's change reference, opaque
)

// ReviewTarget is what to review: a forge, a repository on it and one change.
type ReviewTarget struct {
	Forge      string `json:"forge"`      // "github"; adapters name their own
	Repository string `json:"repository"` // canonical host/owner/name, e.g. github.com/o/r
	Change     string `json:"change"`     // opaque, e.g. "201" for a GitHub PR
}

// ReviewPolicy says what happens to a verdict. A request without one gets
// both true, the operator's choice (#201): published to the forge, and an
// approve outcome as a formal approval.
type ReviewPolicy struct {
	Publish bool `json:"publish"`
	Formal  bool `json:"formal"`
}

// ReviewSpec is an operator's request.
type ReviewSpec struct {
	ReviewTarget
	Reviewer string        `json:"reviewer"` // a local agent ID
	Scope    string        `json:"scope,omitempty"`
	Policy   *ReviewPolicy `json:"policy,omitempty"` // nil: both true
}

// Revisions pin a change: once set on a review they never change; new
// revisions need a new request.
type Revisions struct {
	Base string `json:"base"`
	Head string `json:"head"`
}

// Finding is one remark on the reviewed diff.
type Finding struct {
	Path    string `json:"path"`
	Line    int    `json:"line,omitempty"`
	Summary string `json:"summary"`
}

// Verdict is a reviewer's result for the pinned revisions.
type Verdict struct {
	Outcome  string    `json:"outcome"`
	Summary  string    `json:"summary"`
	Findings []Finding `json:"findings"`
}

// PublicationUpdate is one publication step from the reviewer's side. From
// must equal the current publication state (compare and set). Moving to
// submitting takes the write intent: the broker gives it a new generation,
// bound to the caller's token, and a repeated step with the same token is
// answered as a replay (intent_acquired false), on which nothing may be
// written. Every step out of submitting names that generation, so a late
// answer about an earlier intent is refused.
type PublicationUpdate struct {
	From     string              `json:"from"`
	To       string              `json:"to"`
	Token    string              `json:"token,omitempty"`    // to submitting: the caller's own, at most 128 bytes
	Identity string              `json:"identity,omitempty"` // to submitting: the account the write will publish as, pinned with the intent
	Intent   int                 `json:"intent,omitempty"`   // out of submitting: the generation it holds
	Receipt  *PublicationReceipt `json:"receipt,omitempty"`  // with published
	Reason   string              `json:"reason,omitempty"`   // a fixed class: forbidden, self_review, rate_limited, unavailable, moved, ...
	Observed *Revisions          `json:"observed,omitempty"` // with stale: what the change is now
}

// PublicationReceipt is what the forge confirmed.
type PublicationReceipt struct {
	ReviewID string `json:"review_id"` // the forge's own ID
	URL      string `json:"url,omitempty"`
	Identity string `json:"identity"`        // the account that published
	Revision string `json:"revision"`        // the head the forge recorded
	Formal   bool   `json:"formal"`          // a formal approval or change request, not only a comment
	Event    string `json:"event,omitempty"` // what the forge recorded it as: approve, changes_requested or comment
}

// Publication is a review's publication record.
type Publication struct {
	State    string              `json:"state"`
	Intent   int                 `json:"intent"`             // the newest write intent's generation, 0 before any
	Identity string              `json:"identity,omitempty"` // the account the current intent publishes as
	Receipt  *PublicationReceipt `json:"receipt,omitempty"`
	Reason   string              `json:"reason,omitempty"`
	Updated  *time.Time          `json:"updated_at,omitempty"`
}

// ReviewView is one review request as the broker holds it.
type ReviewView struct {
	ID          string      `json:"review_id"`
	Spec        ReviewSpec  `json:"spec"`
	Requester   string      `json:"requester"` // the operator principal that asked
	State       string      `json:"state"`
	Revisions   *Revisions  `json:"revisions,omitempty"` // from ready on
	Attempt     int         `json:"attempt"`             // 0 until accepted
	Verdict     *Verdict    `json:"verdict,omitempty"`
	Stale       *Revisions  `json:"stale,omitempty"` // the change as last seen, when it moved
	Publication Publication `json:"publication"`
	Reason      string      `json:"reason,omitempty"` // why declined, cancelled or expired
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	ExpiresAt   *time.Time  `json:"expires_at,omitempty"`
}

// ReviewPage is a bounded list of reviews with the total.
type ReviewPage struct {
	Reviews   []ReviewView `json:"reviews"`
	Total     int          `json:"reviews_total"`
	Truncated bool         `json:"reviews_truncated"`
}
