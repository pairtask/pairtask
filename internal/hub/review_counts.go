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
	err := h.readPending(ctx, msg, &out)
	switch {
	case errors.Is(err, store.ErrReviewCursor):
		return (&routingError{protocol.ErrMalformedFrame, err.Error()}).frame(msg.MessageID)
	case err != nil:
		return (&routingError{protocol.ErrInternal, "reviews unavailable"}).frame(msg.MessageID)
	}
	if !historyCapable(msg.ProtocolVersion) {
		untaggedReviews(&out)
	}
	return out
}

// readPending reads the page or the refs a request names into its result.
func (h *Hub) readPending(ctx context.Context, msg protocol.Message, out *protocol.Message) error {
	if msg.ReviewAskRefs != nil {
		page, err := h.store.AskedByRefs(ctx, msg.ReviewAskRefs)
		out.ReviewAsked = &page
		return err
	}
	return pageReaders[msg.ReviewOp](ctx, h.store, pageRequest{msg.ReviewBefore, historyCapable(msg.ProtocolVersion)}, out)
}

// pageRequest is the page a request names: after before, and with every
// pairing's requests for a client with pairing history, else each
// contact's newest pairing's alone (#276).
type pageRequest struct {
	before     string
	allHistory bool
}

// pageReaders read one page of each paged operation into its result.
var pageReaders = map[string]func(context.Context, *store.Store, pageRequest, *protocol.Message) error{
	protocol.ReviewOpAwaiting: func(ctx context.Context, s *store.Store, req pageRequest, out *protocol.Message) error {
		page, err := s.AwaitingReviews(ctx, req.before)
		out.ReviewAwaiting = &page
		return err
	},
	protocol.ReviewOpAsked: func(ctx context.Context, s *store.Store, req pageRequest, out *protocol.Message) error {
		read := s.CurrentAskedReviews
		if req.allHistory {
			read = s.AskedReviews
		}
		page, err := read(ctx, req.before)
		out.ReviewAsked = &page
		return err
	},
	protocol.ReviewOpFailed: func(ctx context.Context, s *store.Store, req pageRequest, out *protocol.Message) error {
		read := s.CurrentFailedReviews
		if req.allHistory {
			read = s.FailedReviews
		}
		page, err := read(ctx, req.before)
		out.ReviewFailed = &page
		return err
	},
}

// pendingRequestFields refuses anything but the operation and its cursor.
func pendingRequestFields(msg protocol.Message) *routingError {
	clean := protocol.Message{Type: msg.Type, MessageID: msg.MessageID, RequestID: msg.RequestID, ProtocolVersion: msg.ProtocolVersion,
		ReviewOp: msg.ReviewOp, ReviewBefore: msg.ReviewBefore, ReviewAskRefs: msg.ReviewAskRefs}
	switch {
	case !sameJSON(msg, clean):
		return &routingError{protocol.ErrMalformedFrame, "review_op " + msg.ReviewOp + " takes only review_before"}
	case msg.ReviewAskRefs == nil:
		return nil
	case msg.ReviewOp != protocol.ReviewOpAsked || msg.ReviewBefore != "":
		return &routingError{protocol.ErrMalformedFrame, "review_ask_refs reads asked requests, without review_before"}
	}
	if err := protocol.ValidateReviewAskRefs(msg.ReviewAskRefs); err != nil {
		return &routingError{protocol.ErrMalformedFrame, err.Error()}
	}
	return refsEpochRefused(msg.ReviewAskRefs, msg.ProtocolVersion)
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
	clean := protocol.Message{Type: msg.Type, MessageID: msg.MessageID, RequestID: msg.RequestID, ProtocolVersion: msg.ProtocolVersion,
		ReviewOp: msg.ReviewOp}
	if !sameJSON(msg, clean) {
		return (&routingError{protocol.ErrMalformedFrame, "review_op counts takes no other field"}).frame(msg.MessageID)
	}
	counts, err := h.readReviewCounts(ctx, msg.ProtocolVersion)
	if err != nil {
		return (&routingError{protocol.ErrInternal, "reviews unavailable"}).frame(msg.MessageID)
	}
	return protocol.Message{Type: protocol.TypeOperatorReviewResult, MessageID: msg.MessageID, ReviewOp: msg.ReviewOp, ReviewCounts: &counts}
}

// readReviewCounts are the review counts a client at version reads: what
// failed counted as its failed page lists it, every pairing's with
// pairing history, else each contact's newest pairing's alone (#276).
func (h *Hub) readReviewCounts(ctx context.Context, version int) (protocol.ReviewCounts, error) {
	if historyCapable(version) {
		return h.store.ReviewCounts(ctx)
	}
	return h.store.CurrentReviewCounts(ctx)
}

// readOnlyReviewOps are the operator's review reads added for #286.
var readOnlyReviewOps = map[string]func(*Hub, context.Context, protocol.Message) protocol.Message{
	protocol.ReviewOpAwaiting: (*Hub).pendingReviews,
	protocol.ReviewOpAsked:    (*Hub).pendingReviews,
	protocol.ReviewOpCounts:   (*Hub).reviewCounts,
	protocol.ReviewOpFailed:   (*Hub).pendingReviews, // #298
	// One asked request with all its findings received (#303).
	protocol.ReviewOpAskedDetail: (*Hub).askedDetail,
}

// askedDetail answers asked_detail: exactly one request, by ID, nothing else.
func (h *Hub) askedDetail(ctx context.Context, msg protocol.Message) protocol.Message {
	clean := protocol.Message{Type: msg.Type, MessageID: msg.MessageID, RequestID: msg.RequestID, ProtocolVersion: msg.ProtocolVersion,
		ReviewOp: msg.ReviewOp, ReviewAskRefs: msg.ReviewAskRefs}
	if !sameJSON(msg, clean) || len(msg.ReviewAskRefs) != 1 {
		return (&routingError{protocol.ErrMalformedFrame, "review_op asked_detail takes exactly one review_ask_refs entry"}).frame(msg.MessageID)
	}
	if err := protocol.ValidateReviewAskRefs(msg.ReviewAskRefs); err != nil {
		return (&routingError{protocol.ErrMalformedFrame, err.Error()}).frame(msg.MessageID)
	}
	if rerr := refsEpochRefused(msg.ReviewAskRefs, msg.ProtocolVersion); rerr != nil {
		return rerr.frame(msg.MessageID)
	}
	page, err := h.store.AskedDetail(ctx, msg.ReviewAskRefs[0])
	if err != nil {
		return (&routingError{protocol.ErrInternal, "reviews unavailable"}).frame(msg.MessageID)
	}
	out := protocol.Message{Type: protocol.TypeOperatorReviewResult, MessageID: msg.MessageID, ReviewOp: msg.ReviewOp, ReviewAsked: &page}
	if !historyCapable(msg.ProtocolVersion) {
		untaggedReviews(&out)
	}
	return out
}
