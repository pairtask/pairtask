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
	if err := admit(v, u); err != nil {
		return r, Step{}, err
	}
	r = apply(r, u, now)
	if v.State == protocol.ReviewCancelled {
		r.View.Publication = cancelPublication(r.View.Publication, now)
	}
	return touched(r, now), Step{Changed: true, Acquired: u.To == protocol.PublicationSubmitting}, nil
}

// admit checks a new step: compare and set on the current state, the
// allowed move, the current intent for a step out of one in flight, and a
// new intent's own conditions.
func admit(v protocol.ReviewView, u protocol.PublicationUpdate) error {
	if u.From != v.Publication.State || !allowed(u.From, u.To) {
		return ErrConflict
	}
	if inFlight(u.From) && u.Intent != v.Publication.Intent {
		return ErrConflict
	}
	if u.To == protocol.PublicationSubmitting {
		return admitIntent(v)
	}
	return nil
}

// admitIntent refuses a write intent once the change moved, or past the
// bound on intents.
func admitIntent(v protocol.ReviewView) error {
	if v.Stale != nil {
		return ErrConflict // nothing more is written from this verdict
	}
	if v.Publication.Intent >= protocol.ReviewWriteIntents {
		return ErrLimit
	}
	return nil
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

// replays recognise a step that already happened, by where it went.
var replays = map[string]func(Record, protocol.PublicationUpdate) bool{
	protocol.PublicationSubmitting: replayedIntent,
	protocol.PublicationStale:      replayedStale,
	protocol.PublicationPublished:  replayedReceipt,
}

// replayed answers a step that already happened: the same token's intent,
// or the same answer about the current intent. It never changes anything.
func replayed(r Record, u protocol.PublicationUpdate) (Step, bool) {
	if check, ok := replays[u.To]; ok {
		return Step{}, check(r, u)
	}
	return Step{}, sameAnswer(r.View.Publication, u)
}

// replayedIntent: this token already holds, or held, the current intent.
func replayedIntent(r Record, u protocol.PublicationUpdate) bool {
	p := r.View.Publication
	if p.Intent == 0 || u.Token != r.Token {
		return false
	}
	return p.State != protocol.PublicationFailed
}

func replayedStale(r Record, u protocol.PublicationUpdate) bool {
	if r.View.Publication.State != protocol.PublicationStale || r.View.Stale == nil {
		return false
	}
	return u.Observed != nil && *r.View.Stale == *u.Observed
}

func replayedReceipt(r Record, u protocol.PublicationUpdate) bool {
	p := r.View.Publication
	if !sameIntent(p, u) || p.Receipt == nil {
		return false
	}
	return u.Receipt != nil && *p.Receipt == *u.Receipt
}

// sameAnswer: the current intent already ended this way, for this reason.
func sameAnswer(p protocol.Publication, u protocol.PublicationUpdate) bool {
	if !sameIntent(p, u) {
		return false
	}
	return u.Reason == p.Reason
}

func sameIntent(p protocol.Publication, u protocol.PublicationUpdate) bool {
	return u.To == p.State && u.Intent == p.Intent
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
		if moved := receiptMismatch(r.View, receipt); moved != nil {
			r.View.Stale = moved // the receipt stays; the mismatch shows
		}
	case protocol.PublicationStale:
		observed := *u.Observed
		r.View.Stale = &observed
	}
	return r
}

// receiptMismatch is the change as the forge recorded it, when that is not
// the pinned head and nothing else marked the review stale yet.
func receiptMismatch(v protocol.ReviewView, receipt protocol.PublicationReceipt) *protocol.Revisions {
	pinned := v.Revisions
	if pinned == nil || v.Stale != nil {
		return nil
	}
	if receipt.Revision == pinned.Head {
		return nil
	}
	return &protocol.Revisions{Base: pinned.Base, Head: receipt.Revision}
}
