// Package review is the lifecycle of a review request (#201): pure steps on
// one record, which the store applies in a transaction. A step reports
// whether it changed anything, so a repeated request is answered without a
// second change, and refuses anything that no longer holds.
package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Errors the broker maps to protocol answers.
var (
	// ErrNotFound: no such review for this agent. A review assigned to
	// someone else reads the same, so nothing reveals it exists.
	ErrNotFound = errors.New("no such review for this agent")
	// ErrConflict: the state, attempt, revisions or intent no longer hold.
	ErrConflict = errors.New("the review is not in a state that allows this")
	// ErrIDReused: a request ID already names a different request.
	ErrIDReused = errors.New("this request ID already names a different review request")
	// ErrFull: the bound on open reviews, in all or for this reviewer.
	ErrFull = errors.New("too many open review requests")
	// ErrLimit: this review used up its attempts or write intents.
	ErrLimit = errors.New("this review has no attempts or publication retries left")
	// ErrMoved: the change no longer has the revisions its request pinned.
	ErrMoved = errors.New("the change moved since the request pinned it; decline the review")
	// ErrDenied: publishing is no longer allowed for this review.
	ErrDenied = errors.New("publishing is no longer allowed by this workspace")
)

// Record is a review as stored: the view everyone sees, the token bound to
// the current write intent, and the request's fingerprint for retries.
type Record struct {
	View  protocol.ReviewView
	Token string
	Hash  string
}

// Open reports a review that still waits for or runs a review.
func Open(state string) bool {
	return state == protocol.ReviewRequested || state == protocol.ReviewReady || state == protocol.ReviewInReview
}

// ended reports a review that stopped without a verdict.
func ended(state string) bool {
	return state == protocol.ReviewDeclined || state == protocol.ReviewCancelled || state == protocol.ReviewExpired
}

// DefaultPolicy is the operator's choice (#201): publish, and approve formally.
func DefaultPolicy() protocol.ReviewPolicy { return protocol.ReviewPolicy{Publish: true, Formal: true} }

// New is a requested review. The policy is resolved here, so a stored review
// always carries one.
func New(id, requester string, spec protocol.ReviewSpec, now time.Time) Record {
	policy := DefaultPolicy()
	if spec.Policy != nil {
		policy = *spec.Policy
	}
	spec.Policy = &policy
	expires := now.Add(protocol.ReviewUnaccepted)
	view := protocol.ReviewView{ID: id, Spec: spec, Requester: requester, State: protocol.ReviewRequested,
		Publication: protocol.Publication{State: protocol.PublicationNone}, CreatedAt: now, UpdatedAt: now, ExpiresAt: &expires}
	return Record{View: view, Hash: Hash(requester, spec)}
}

// Hash fingerprints a request with its policy resolved, so a retry that
// omits the default policy matches one that states it.
func Hash(requester string, spec protocol.ReviewSpec) string {
	policy := DefaultPolicy()
	if spec.Policy != nil {
		policy = *spec.Policy
	}
	spec.Policy = &policy
	data, _ := json.Marshal(struct {
		Requester string              `json:"requester"`
		Spec      protocol.ReviewSpec `json:"spec"`
	}{requester, spec})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Expire ends an open review whose time is up.
func Expire(r Record, now time.Time) (Record, bool) {
	if !due(r.View, now) {
		return r, false
	}
	return finish(r, protocol.ReviewExpired, "expired", now), true
}

// due reports an open review whose time is up.
func due(v protocol.ReviewView, now time.Time) bool {
	if !Open(v.State) || v.ExpiresAt == nil {
		return false
	}
	return !now.Before(*v.ExpiresAt)
}

// Prepare pins the revisions the reviewer resolved: requested → ready. The
// same revisions again change nothing; different ones never replace them.
func Prepare(r Record, revisions protocol.Revisions, now time.Time) (Record, bool, error) {
	v := r.View
	if v.Pinned != nil && revisions != *v.Pinned {
		return r, false, ErrMoved // a request's pins are never replaced
	}
	if v.Revisions != nil {
		if *v.Revisions == revisions && !ended(v.State) {
			return r, false, nil
		}
		return r, false, ErrConflict
	}
	if v.State != protocol.ReviewRequested {
		return r, false, ErrConflict
	}
	r.View.Revisions, r.View.State = &revisions, protocol.ReviewReady
	return touched(r, now), true, nil
}

// Accept dispatches the review of a ready request: attempt 1, and the time
// to finish it. Repeating it while the review runs changes nothing.
func Accept(r Record, now time.Time) (Record, bool, error) {
	switch r.View.State {
	case protocol.ReviewInReview:
		return r, false, nil
	case protocol.ReviewReady:
	default:
		return r, false, ErrConflict
	}
	expires := now.Add(protocol.ReviewUnfinished)
	r.View.State, r.View.Attempt, r.View.ExpiresAt = protocol.ReviewInReview, 1, &expires
	return touched(r, now), true, nil
}

// Decline ends a review the reviewer won't do, before it runs.
func Decline(r Record, reason string, now time.Time) (Record, bool, error) {
	switch r.View.State {
	case protocol.ReviewDeclined:
		return r, false, nil
	case protocol.ReviewRequested, protocol.ReviewReady:
		return finish(r, protocol.ReviewDeclined, reason, now), true, nil
	}
	return r, false, ErrConflict
}

// Cancel ends a review for the operator. An open one gets no verdict. A
// completed one keeps its verdict, and nothing new is published from it: a
// pending or failed publication is cancelled, one in flight may still be
// reconciled, and what was published stays published.
func Cancel(r Record, reason string, now time.Time) (Record, bool, error) {
	switch r.View.State {
	case protocol.ReviewCancelled:
		return r, false, nil
	case protocol.ReviewCompleted:
		r = finish(r, protocol.ReviewCancelled, reason, now)
		r.View.Publication = cancelPublication(r.View.Publication, now)
		return r, true, nil
	}
	if Open(r.View.State) {
		return finish(r, protocol.ReviewCancelled, reason, now), true, nil
	}
	return r, false, ErrConflict
}

func cancelPublication(p protocol.Publication, now time.Time) protocol.Publication {
	if p.State == protocol.PublicationPending || p.State == protocol.PublicationFailed {
		p.State, p.Updated = protocol.PublicationCancelled, &now
	}
	return p
}

// Restart replaces the current attempt with a new one on the same revisions.
func Restart(r Record, attempt int, now time.Time) (Record, bool, error) {
	if r.View.State != protocol.ReviewInReview || attempt != r.View.Attempt {
		return r, false, ErrConflict
	}
	if r.View.Attempt >= protocol.ReviewAttempts {
		return r, false, ErrLimit
	}
	r.View.Attempt++
	return touched(r, now), true, nil
}

// Submit records the current attempt's verdict: in_review → completed, with
// publication pending when the policy publishes. The same verdict again
// changes nothing.
func Submit(r Record, attempt int, verdict protocol.Verdict, now time.Time) (Record, bool, error) {
	v := r.View
	if resubmitted(v, attempt, verdict) {
		return r, false, nil
	}
	if v.State != protocol.ReviewInReview || attempt != v.Attempt {
		return r, false, ErrConflict
	}
	if verdict.Findings == nil {
		verdict.Findings = []protocol.Finding{}
	}
	r.View.State, r.View.Verdict, r.View.ExpiresAt = protocol.ReviewCompleted, &verdict, nil
	r.View.Publication = protocol.Publication{State: firstPublication(v.Spec.Policy), Updated: &now}
	return touched(r, now), true, nil
}

// resubmitted: this attempt already completed with this verdict.
func resubmitted(v protocol.ReviewView, attempt int, verdict protocol.Verdict) bool {
	if v.State != protocol.ReviewCompleted || attempt != v.Attempt {
		return false
	}
	return sameVerdict(v.Verdict, verdict)
}

// firstPublication is pending when the policy publishes, else none.
func firstPublication(policy *protocol.ReviewPolicy) string {
	if policy != nil && policy.Publish {
		return protocol.PublicationPending
	}
	return protocol.PublicationNone
}

// Observe records the change as the reviewer's side sees it now, after a
// verdict: if base or head moved, the verdict is stale. It never touches the
// publication, so a published receipt stays.
func Observe(r Record, attempt int, observed protocol.Revisions, now time.Time) (Record, bool, error) {
	v := r.View
	if !observable(v, attempt) {
		return r, false, ErrConflict
	}
	if alreadySeen(v, observed) {
		return r, false, nil
	}
	r.View.Stale = &observed
	return touched(r, now), true, nil
}

// observable: a verdict on pinned revisions, from the current attempt.
func observable(v protocol.ReviewView, attempt int) bool {
	if v.Verdict == nil || v.Revisions == nil {
		return false
	}
	return attempt == v.Attempt
}

// alreadySeen: the pins themselves, or the change as last recorded.
func alreadySeen(v protocol.ReviewView, observed protocol.Revisions) bool {
	if observed == *v.Revisions {
		return true
	}
	return v.Stale != nil && *v.Stale == observed
}

func sameVerdict(a *protocol.Verdict, b protocol.Verdict) bool {
	if a == nil {
		return false
	}
	x, _ := json.Marshal(a)
	if b.Findings == nil {
		b.Findings = []protocol.Finding{}
	}
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func touched(r Record, now time.Time) Record {
	r.View.UpdatedAt = now
	return r
}

func finish(r Record, state, reason string, now time.Time) Record {
	r.View.State, r.View.Reason, r.View.ExpiresAt = state, reason, nil
	return touched(r, now)
}
