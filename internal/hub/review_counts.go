package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// Pending reviews for the operator (#286): the snapshot counts them, and
// operator_review pages the requests awaiting the operator and this side's
// requests to paired workspaces. Both are read only.

// pendingReviews answers awaiting, asked or failed with one page after
// review_before.
func (h *Hub) pendingReviews(ctx context.Context, msg protocol.Message) protocol.Message {
	if rerr := pendingRequestFields(msg); rerr != nil {
		return rerr.frame(msg.MessageID)
	}
	out := protocol.Message{Type: protocol.TypeOperatorReviewResult, MessageID: msg.MessageID, ReviewOp: msg.ReviewOp}
	err := pageReaders[msg.ReviewOp](ctx, h.store, msg.ReviewBefore, &out)
	switch {
	case errors.Is(err, store.ErrReviewCursor):
		return (&routingError{protocol.ErrMalformedFrame, err.Error()}).frame(msg.MessageID)
	case err != nil:
		return (&routingError{protocol.ErrInternal, "reviews unavailable"}).frame(msg.MessageID)
	}
	return out
}

// pageReaders read one page of each paged operation into its result.
var pageReaders = map[string]func(context.Context, *store.Store, string, *protocol.Message) error{
	protocol.ReviewOpAwaiting: func(ctx context.Context, s *store.Store, before string, out *protocol.Message) error {
		page, err := s.AwaitingReviews(ctx, before)
		out.ReviewAwaiting = &page
		return err
	},
	protocol.ReviewOpAsked: func(ctx context.Context, s *store.Store, before string, out *protocol.Message) error {
		page, err := s.AskedReviews(ctx, before)
		out.ReviewAsked = &page
		return err
	},
	protocol.ReviewOpFailed: func(ctx context.Context, s *store.Store, before string, out *protocol.Message) error {
		page, err := s.FailedReviews(ctx, before)
		out.ReviewFailed = &page
		return err
	},
}

// pendingRequestFields refuses anything but the operation and its cursor.
func pendingRequestFields(msg protocol.Message) *routingError {
	clean := protocol.Message{Type: msg.Type, MessageID: msg.MessageID, RequestID: msg.RequestID, ReviewOp: msg.ReviewOp,
		ReviewBefore: msg.ReviewBefore}
	if !sameJSON(msg, clean) {
		return &routingError{protocol.ErrMalformedFrame, "review_op " + msg.ReviewOp + " takes only review_before"}
	}
	return nil
}

// sameJSON: the two frames encode alike, so one carries nothing the other
// leaves out.
func sameJSON(a, b protocol.Message) bool {
	x, errX := json.Marshal(a)
	y, errY := json.Marshal(b)
	return errX == nil && errY == nil && bytes.Equal(x, y)
}

// reviewCounts answers counts: the snapshot's review_counts, read again.
func (h *Hub) reviewCounts(ctx context.Context, msg protocol.Message) protocol.Message {
	clean := protocol.Message{Type: msg.Type, MessageID: msg.MessageID, RequestID: msg.RequestID, ReviewOp: msg.ReviewOp}
	if !sameJSON(msg, clean) {
		return (&routingError{protocol.ErrMalformedFrame, "review_op counts takes no other field"}).frame(msg.MessageID)
	}
	counts, err := h.store.ReviewCounts(ctx)
	if err != nil {
		return (&routingError{protocol.ErrInternal, "reviews unavailable"}).frame(msg.MessageID)
	}
	return protocol.Message{Type: protocol.TypeOperatorReviewResult, MessageID: msg.MessageID, ReviewOp: msg.ReviewOp, ReviewCounts: &counts}
}

// readOnlyReviewOps are the operator's review reads added for #286.
var readOnlyReviewOps = map[string]func(*Hub, context.Context, protocol.Message) protocol.Message{
	protocol.ReviewOpAwaiting: (*Hub).pendingReviews,
	protocol.ReviewOpAsked:    (*Hub).pendingReviews,
	protocol.ReviewOpCounts:   (*Hub).reviewCounts,
	protocol.ReviewOpFailed:   (*Hub).pendingReviews, // #298
}
