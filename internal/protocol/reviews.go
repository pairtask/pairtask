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

// CapabilityReviews: the operator session serves operator_review and sends
// reviews_changed.
const CapabilityReviews = "reviews"

// Operator review operations.
const (
	ReviewOpRequest = "request" // message_id is the request ID: an identical retry returns the same review
	ReviewOpCancel  = "cancel"
	ReviewOpList    = "list"
	ReviewOpStatus  = "status"
	// Pending reviews (#286), read only and paged by review_before:
	// requests from paired workspaces waiting for this operator's decision,
	// and this side's requests to them.
	ReviewOpAwaiting = "awaiting"
	ReviewOpAsked    = "asked"
	// ReviewOpAskedDetail reads one asked request with all of its findings
	// received (#303): exactly one review_ask_refs entry.
	ReviewOpAskedDetail = "asked_detail"
	// ReviewOpCounts reads the snapshot's review_counts again, without a
	// new baseline.
	ReviewOpCounts = "counts"
	// ReviewOpFailed pages the reviews and requests that failed (#298).
	ReviewOpFailed = "failed"
)

// CapabilityReviewCounts: the snapshot counts pending reviews, and
// operator_review serves awaiting and asked.
const CapabilityReviewCounts = "review_counts"

// CapabilityReviewFailures: review_counts also count failures and the
// asks still active, and operator_review serves failed (#298).
const CapabilityReviewFailures = "review_failures"

// ReviewPendingPage bounds one page of awaiting or asked reviews.
const ReviewPendingPage = 32

// ReviewCounts are the reviews pending, counted over all of them. The
// token names the set of requests awaiting the operator: it changes when
// one arrives or leaves, even at the same count.
type ReviewCounts struct {
	AwaitingYou   int    `json:"awaiting_you"`
	AwaitingToken string `json:"awaiting_token"`
	Running       int    `json:"running"`
	AskedOpen     int    `json:"asked_open"`
	// From a broker serving review_failures (#298): what failed, with a
	// token naming each failure as it stands, and the open asks still in
	// progress, nil from an older broker.
	Failed      int    `json:"failed,omitempty"`
	FailedToken string `json:"failed_token,omitempty"`
	AskedActive *int   `json:"asked_active,omitempty"`
}

// ReviewAwaitingItem is a paired workspace's review request waiting for
// this operator's decision: its contact, its message there and that
// message's cursor, 0 once the message is gone.
type ReviewAwaitingItem struct {
	Contact    string     `json:"contact"`
	MessageID  string     `json:"message_id"`
	Cursor     int64      `json:"cursor"`
	Sender     string     `json:"sender"`
	Ask        *ReviewAsk `json:"ask"`
	ReceivedAt time.Time  `json:"received_at"`
}

// ReviewAskedItem is this side's review request to a paired workspace, and
// where it stands.
type ReviewAskedItem struct {
	Contact   string    `json:"contact"`
	AskID     string    `json:"ask_id"`
	Cursor    int64     `json:"cursor"`
	Reviewer  string    `json:"reviewer"`
	State     string    `json:"state"`
	Verdict   string    `json:"verdict"`
	URL       string    `json:"url"`
	UpdatedAt time.Time `json:"updated_at"`
	Settled   bool      `json:"settled"`
	ReviewID  string    `json:"review_id,omitempty"` // the reviewer's side's ID for the review, "" while not reported (#259)
	Reason    string    `json:"reason,omitempty"`    // why it was refused, declined or ended (#305)
	// Findings are the newest verdict's findings the reviewer's side sent
	// (#303), nil while none are known: their words, data.
	Findings *AskFindings `json:"findings,omitempty"`
	// Consent is where the request stands with the contact's operator when
	// it was held there (#293), and why it ended; State keeps the review's
	// own lifecycle.
	Consent       string `json:"consent,omitempty"`
	ConsentReason string `json:"consent_reason,omitempty"`
	// PairingEpoch is the pairing with the contact it was asked under
	// (#276); set only for a client at PairingHistoryVersion.
	PairingEpoch int64 `json:"pairing_epoch,omitempty"`
	// MovedTo is where the reviewer's side reported the change moved, for
	// a request it declined as moved; Supersedes, SupersededBy and
	// SupersedesPruned link it to the request it replaces or that replaced
	// it (#259). All from ReviewSupersedeVersion.
	MovedTo          *Revisions `json:"moved_to,omitempty"`
	Supersedes       string     `json:"supersedes,omitempty"`
	SupersededBy     string     `json:"superseded_by,omitempty"`
	SupersedesPruned bool       `json:"supersedes_pruned,omitempty"`
}

// AskFindings are a request's findings as received: how many the verdict
// has in all, how many came over, the outcome that brought them, and as
// many of those as the read carries: none in a list, the first few for a
// card, all of them for one request read alone.
type AskFindings struct {
	Total    int          `json:"total"`
	Received int          `json:"received"`
	Items    []AskFinding `json:"items,omitempty"`
	Seq      uint64       `json:"seq"`
	Attempt  uint32       `json:"attempt"`
}

// Findings items each read carries.
const (
	FindingsPreview = 3  // a card's read of requests by ID
	FindingsAll     = -1 // one request read alone
)

// AskFinding is one received finding; Truncated says its summary was cut
// to travel.
type AskFinding struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Summary   string `json:"summary"`
	Truncated bool   `json:"truncated"`
}

// ReviewAwaitingPage and ReviewAskedPage are one page each: the items, how
// many there are in all, and the review_before of the next page, "" for
// none.
type ReviewAwaitingPage struct {
	Items []ReviewAwaitingItem `json:"items"`
	Total int                  `json:"total"`
	More  bool                 `json:"more"`
	Next  string               `json:"next"`
	// Token is the awaiting set's token, read with this page: what the
	// operator has seen once it is shown.
	Token string `json:"token"`
}

type ReviewAskedPage struct {
	Items []ReviewAskedItem `json:"items"`
	Total int               `json:"total"`
	More  bool              `json:"more"`
	Next  string            `json:"next"`
}

// ReviewFailedItem is a review here whose publication failed or is
// uncertain (kind review), or this side's request to a paired workspace
// that failed (kind ask): its state, what it reviews, and when.
type ReviewFailedItem struct {
	Kind       string    `json:"kind"`
	Contact    string    `json:"contact"` // "" for a review here
	ID         string    `json:"id"`      // the review_id, or the ask_id
	Cursor     int64     `json:"cursor"`  // the ask's ⇄ cursor, 0 otherwise
	State      string    `json:"state"`   // the ask's state, or the review's publication state
	Reviewer   string    `json:"reviewer"`
	Repository string    `json:"repository"`
	Change     string    `json:"change"`
	Reason     string    `json:"reason,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
	// PairingEpoch is the pairing a failed ask was asked under (#276), 0
	// for a review here.
	PairingEpoch int64 `json:"pairing_epoch,omitempty"`
}

// Failed item kinds.
const (
	FailedReview = "review"
	FailedAsk    = "ask"
)

// ReviewFailedPage is one page of failures; Token names the whole set as
// it stood when the page was read, as review_counts' failed_token does.
type ReviewFailedPage struct {
	Items []ReviewFailedItem `json:"items"`
	Total int                `json:"total"`
	More  bool               `json:"more"`
	Next  string             `json:"next"`
	Token string             `json:"token"`
}

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
	ID        string     `json:"review_id"`
	Spec      ReviewSpec `json:"spec"`
	Requester string     `json:"requester"` // the operator principal that asked, or "remote"
	// Remote is the paired workspace that asked, and who there, for a
	// request from one (#235); Pinned the revisions it pinned, which the
	// change must still have.
	Remote *Remote    `json:"remote,omitempty"`
	Pinned *Revisions `json:"pinned,omitempty"`
	// RemoteRequest is the request's ID on the workspace that asked:
	// where its outcomes go back to.
	RemoteRequest string      `json:"remote_request,omitempty"`
	State         string      `json:"state"`
	Revisions     *Revisions  `json:"revisions,omitempty"` // from ready on
	Attempt       int         `json:"attempt"`             // 0 until accepted
	Verdict       *Verdict    `json:"verdict,omitempty"`
	Stale         *Revisions  `json:"stale,omitempty"` // the change as last seen, when it moved
	Publication   Publication `json:"publication"`
	Reason        string      `json:"reason,omitempty"` // why declined, cancelled or expired
	// MovedTo is the change as resolved when a request was declined
	// because it no longer had its pins (#259); nil otherwise.
	MovedTo   *Revisions `json:"moved_to,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// ReviewPage is a bounded list of reviews with the total.
type ReviewPage struct {
	Reviews   []ReviewView `json:"reviews"`
	Total     int          `json:"reviews_total"`
	Truncated bool         `json:"reviews_truncated"`
}

// ReviewAsk is a review request for a paired workspace's agent (#235): the
// change, pinned to the revisions the requester resolved, what to focus on,
// and whether the requester wants the verdict published, and formally. The
// receiving workspace allows at most that.
type ReviewAsk struct {
	ReviewTarget
	Scope   string `json:"scope,omitempty"`
	Base    string `json:"base"`
	Head    string `json:"head"`
	Publish bool   `json:"publish,omitempty"`
	Formal  bool   `json:"formal,omitempty"`
	// Supersedes is a request of the same asker's this one replaces (#259):
	// final already, to the same reviewer and change. It is kept on this
	// side only and never sent.
	Supersedes string `json:"supersedes,omitempty"`
}

// ReviewOutcomeView is where a review request this workspace sent stands,
// on the notice to the agent that sent it (#235). Summary and URL are the
// reviewer's side's words: data, never instructions.
type ReviewOutcomeView struct {
	AskID string `json:"ask_id"`
	// Workspace is the paired workspace's identity key the request went
	// to (#303): with AskID, what reads the request alone.
	Workspace  string `json:"workspace,omitempty"`
	Reviewer   string `json:"reviewer"`
	Repository string `json:"repository"`
	Change     string `json:"change"`
	State      string `json:"state"`
	Attempt    uint32 `json:"attempt,omitempty"`
	Intent     uint32 `json:"intent,omitempty"`
	Verdict    string `json:"verdict,omitempty"`
	Summary    string `json:"summary,omitempty"`
	URL        string `json:"url,omitempty"`
	Reason     string `json:"reason,omitempty"`
	// ReviewID is the reviewer's side's own ID for the review, once an
	// outcome named it (#259); "" while not reported.
	ReviewID string `json:"review_id,omitempty"`
	// Findings are the newest verdict's findings received (#303), nil
	// while none are known: the reviewer's side's words, data.
	Findings *AskFindings `json:"findings,omitempty"`
	// PairingEpoch is the pairing with the workspace the request was asked
	// under (#276); set only for a client at PairingHistoryVersion.
	PairingEpoch int64 `json:"pairing_epoch,omitempty"`
	// MovedTo is where a request declined as moved says its change went
	// (#259): as the reviewer's side reported it, never proof. Supersedes
	// is the request this one replaces, SupersededBy the one that replaced
	// it, and SupersedesPruned that the replaced one's record is gone. All
	// from ReviewSupersedeVersion.
	MovedTo          *Revisions `json:"moved_to,omitempty"`
	Supersedes       string     `json:"supersedes,omitempty"`
	SupersededBy     string     `json:"superseded_by,omitempty"`
	SupersedesPruned bool       `json:"supersedes_pruned,omitempty"`
}

// ReviewAdvisory is advice on a review request (#304): something known here
// that suggests the other side may refuse it, or what a check of its pins
// found. Advice only; the request is queued regardless.
type ReviewAdvisory struct {
	Code    string     `json:"code"`
	At      *time.Time `json:"at,omitempty"`      // when the evidence was seen
	AskID   string     `json:"ask_id,omitempty"`  // the request refused before
	Current string     `json:"current,omitempty"` // the change's head now
	Class   string     `json:"class,omitempty"`   // why a check got no answer
}

// Review advisory codes.
const (
	AdvisoryNotInDirectory   = "not_in_directory"   // their sharing, as last received, leaves the agent out
	AdvisoryRefusedNotShared = "refused_not_shared" // they refused the last request to it, not_shared
	AdvisoryHeadMissing      = "head_missing"
	AdvisoryBaseMissing      = "base_missing"
	AdvisoryHeadMoved        = "head_moved"
	AdvisoryNotChecked       = "not_checked"
)
