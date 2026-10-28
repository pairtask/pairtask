package review

import (
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// publicationSteps are the allowed moves, each from one state to another.
//
//	pending → submitting | stale
//	submitting → published | failed | uncertain
//	failed → submitting | stale
//	uncertain → published
var publicationSteps = map[string][]string{
	protocol.PublicationPending:    {protocol.PublicationSubmitting, protocol.PublicationStale},
	protocol.PublicationSubmitting: {protocol.PublicationPublished, protocol.PublicationFailed, protocol.PublicationUncertain},
	protocol.PublicationFailed:     {protocol.PublicationSubmitting, protocol.PublicationStale},
	protocol.PublicationUncertain:  {protocol.PublicationPublished},
}

// Step is one publication step's outcome.
type Step struct {
	Changed bool
	// Acquired: this call took the write intent, so its caller may write to
	// the forge. A replay of the same token never acquires it again.
	Acquired bool
}

// Publish applies one publication step of the current attempt's verdict.
// From must equal the current state (compare and set). A step to
// submitting takes a new write intent bound to the caller's token; every
// step after it names that intent, so a late answer about an earlier one is
// refused. A repeated step answers what already happened, changing nothing.
func Publish(r Record, attempt int, u protocol.PublicationUpdate, now time.Time) (Record, Step, error) {
	v := r.View
	if attempt != v.Attempt || !publishable(v.State, u) {
		return r, Step{}, ErrConflict
	}
	if replay, ok := replayed(r, u); ok {
		return r, replay, nil
	}
	if u.From != v.Publication.State || !allowed(u.From, u.To) {
		return r, Step{}, ErrConflict
	}
	if inFlight(u.From) && u.Intent != v.Publication.Intent {
		return r, Step{}, ErrConflict
	}
	if u.To == protocol.PublicationSubmitting && v.Stale != nil {
		return r, Step{}, ErrConflict // the change moved: nothing more is written from this verdict
	}
	if u.To == protocol.PublicationSubmitting && v.Publication.Intent >= protocol.ReviewWriteIntents {
		return r, Step{}, ErrLimit
	}
	r = apply(r, u, now)
	if v.State == protocol.ReviewCancelled {
		r.View.Publication = cancelPublication(r.View.Publication, now)
	}
	return touched(r, now), Step{Changed: true, Acquired: u.To == protocol.PublicationSubmitting}, nil
}

// publishable: a completed review takes every step; a cancelled one only
// reconciles a write already in flight.
func publishable(state string, u protocol.PublicationUpdate) bool {
	if state == protocol.ReviewCompleted {
		return true
	}
	return state == protocol.ReviewCancelled && inFlight(u.From)
}

func inFlight(state string) bool {
	return state == protocol.PublicationSubmitting || state == protocol.PublicationUncertain
}

// replayed answers a step that already happened: the same token's intent,
// or the same final answer about the current intent.
func replayed(r Record, u protocol.PublicationUpdate) (Step, bool) {
	p := r.View.Publication
	switch {
	case u.To == protocol.PublicationSubmitting:
		return Step{}, p.Intent > 0 && u.Token == r.Token && p.State != protocol.PublicationFailed
	case u.To == protocol.PublicationStale:
		return Step{}, p.State == protocol.PublicationStale && r.View.Stale != nil && u.Observed != nil && *r.View.Stale == *u.Observed
	case u.To != p.State || u.Intent != p.Intent:
		return Step{}, false
	case u.To == protocol.PublicationPublished:
		return Step{}, p.Receipt != nil && u.Receipt != nil && *p.Receipt == *u.Receipt
	}
	return Step{}, u.Reason == p.Reason
}

func allowed(from, to string) bool {
	for _, next := range publicationSteps[from] {
		if next == to {
			return true
		}
	}
	return false
}

func apply(r Record, u protocol.PublicationUpdate, now time.Time) Record {
	p := &r.View.Publication
	p.State, p.Reason, p.Updated = u.To, u.Reason, &now
	switch u.To {
	case protocol.PublicationSubmitting:
		p.Intent++
		r.Token = u.Token
	case protocol.PublicationPublished:
		receipt := *u.Receipt
		p.Receipt = &receipt
		if pinned := r.View.Revisions; pinned != nil && receipt.Revision != pinned.Head && r.View.Stale == nil {
			// The forge recorded another head: the receipt stays, the
			// mismatch shows.
			r.View.Stale = &protocol.Revisions{Base: pinned.Base, Head: receipt.Revision}
		}
	case protocol.PublicationStale:
		observed := *u.Observed
		r.View.Stale = &observed
	}
	return r
}
