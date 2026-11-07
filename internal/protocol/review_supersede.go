package protocol

import "strings"

// Stale review requests (#259): a request a paired workspace asked for whose
// change moved before it was accepted is declined there, with a reason in a
// fixed form saying where it moved, and the side that asked may ask again,
// naming the request it replaces. A replacement is linked on the asking side
// only; nothing new travels between workspaces.

const (
	// ReviewSupersedeVersion: review_check_pins, moved_to on reviews and
	// outcomes, and review_ask.supersedes with the links it makes.
	ReviewSupersedeVersion = 26
	// TypeReviewCheckPins: agent → broker, the change as resolved now for a
	// requested or ready review with pins. The same pins change nothing;
	// others decline it as moved.
	TypeReviewCheckPins = "review_check_pins"
	// CapabilityReviewSupersede: the broker takes review_ask.supersedes from
	// the operator.
	CapabilityReviewSupersede = "review_supersede"
	// ErrNotSupersedable: the request named can't be replaced by this one.
	ErrNotSupersedable = "not_supersedable"
)

// movedPrefix and movedHead frame a moved reason around its two revisions.
const (
	movedPrefix = "moved base="
	movedHead   = " head="
)

// MovedReason is the reason a review is declined with when its change moved:
// exactly this form, so the side that asked can read where to.
func MovedReason(to Revisions) string {
	return movedPrefix + to.Base + movedHead + to.Head
}

// MovedTo reads a moved reason: the revisions it names, or nil for any other
// text. Only the exact form with two full commit IDs reads; it is what the
// other side reported, never proof of the change.
func MovedTo(reason string) *Revisions {
	rest, ok := strings.CutPrefix(reason, movedPrefix)
	if !ok {
		return nil
	}
	base, head, ok := strings.Cut(rest, movedHead)
	if !ok {
		return nil
	}
	to := Revisions{Base: base, Head: head}
	if ValidateRevisions(&to) != nil {
		return nil
	}
	return &to
}

// OutcomeFor is a message's outcome, if any, as a client at version reads it.
func OutcomeFor(v *ReviewOutcomeView, version int) *ReviewOutcomeView {
	if v == nil {
		return nil
	}
	trimmed := v.ForVersion(version)
	return &trimmed
}

// ReviewsFor is a frame's reviews and requests as a client at version
// reads them: without what came after that version.
func ReviewsFor(m Message, version int) Message {
	if version >= ReviewSupersedeVersion {
		return m
	}
	if m.Review != nil {
		view := ReviewFor(*m.Review, version)
		m.Review = &view
	}
	if m.ReviewPage != nil {
		page := *m.ReviewPage
		page.Reviews = mapped(page.Reviews, func(v ReviewView) ReviewView { return ReviewFor(v, version) })
		m.ReviewPage = &page
	}
	if m.ReviewAsked != nil {
		page := *m.ReviewAsked
		page.Items = mapped(page.Items, func(i ReviewAskedItem) ReviewAskedItem { return i.ForVersion(version) })
		m.ReviewAsked = &page
	}
	return m
}

// mapped is a copy of items, each through f; nil stays nil.
func mapped[T any](items []T, f func(T) T) []T {
	if items == nil {
		return nil
	}
	out := make([]T, len(items))
	for i, item := range items {
		out[i] = f(item)
	}
	return out
}

// ReviewFor is a review as a client at version reads it.
func ReviewFor(v ReviewView, version int) ReviewView {
	if version < ReviewSupersedeVersion {
		v.MovedTo = nil
	}
	return v
}

// ForVersion is a request as a client at version reads it.
func (i ReviewAskedItem) ForVersion(version int) ReviewAskedItem {
	if version < ReviewSupersedeVersion {
		i.MovedTo, i.Supersedes, i.SupersededBy, i.SupersedesPruned = nil, "", "", false
	}
	return i
}

// ForVersion is the outcome as a client at version reads it: without what
// came after that version.
func (v ReviewOutcomeView) ForVersion(version int) ReviewOutcomeView {
	if version < ReviewSupersedeVersion {
		v.MovedTo, v.Supersedes, v.SupersededBy, v.SupersedesPruned = nil, "", "", false
	}
	return v
}
