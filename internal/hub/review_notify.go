package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// A new review request reaches its reviewer the way an operator's message
// does (#220): one durable operator message, stored with the request, pushed
// to the reviewer if connected and replayed otherwise. Its ID derives from
// the review's, so a retried request never writes a second one; the review
// ID it carries is what a reviewer deduplicates on.

// reviewNoticePrefix starts every review notice's message ID.
const reviewNoticePrefix = "review:"

// reviewNotice is the notice for a new request, or nil for a retry of one
// already stored or anything that isn't a request. The reviewer must be
// able to receive operator messages; otherwise the request is refused
// before anything is audited as accepted or stored.
func (h *Hub) reviewNotice(ctx context.Context, clients map[string]*Client, msg protocol.Message) (*store.ReviewNotice, *routingError) {
	if msg.ReviewOp != protocol.ReviewOpRequest {
		return nil, nil
	}
	_, stored, err := h.store.GetReview(ctx, msg.MessageID)
	if err != nil {
		return nil, &routingError{protocol.ErrInternal, "reviews unavailable"}
	}
	if stored {
		return nil, nil // a retry answers with what is stored; its notice exists already
	}
	reviewer, rerr := h.operatorMember(ctx, clients, msg.ReviewSpec.Reviewer)
	if rerr != nil {
		return nil, rerr
	}
	out, err := reviewNoticeFrame(msg, h.seq+1)
	if err != nil {
		return nil, &routingError{protocol.ErrInternal, "review notice not encodable"}
	}
	if rerr := checkFrameSize(out); rerr != nil {
		return nil, rerr
	}
	return &store.ReviewNotice{Msg: out, Recipients: []protocol.Recipient{reviewer}}, nil
}

// reviewNoticeFrame is the reviewer's message about a request.
func reviewNoticeFrame(msg protocol.Message, seq uint64) (protocol.Message, error) {
	payload, err := json.Marshal(map[string]string{"text": reviewNoticeText(msg)})
	if err != nil {
		return protocol.Message{}, err
	}
	ts := time.Now().UTC()
	return protocol.Message{Type: protocol.TypeMsg, Seq: seq, TS: &ts, From: protocol.OperatorID, To: msg.ReviewSpec.Reviewer,
		Origin: protocol.OriginOperator, Payload: payload, MessageID: reviewNoticeID(msg.MessageID), AckRequested: true,
		Durable: true, ReviewID: msg.MessageID}, nil
}

// reviewNoticeID is the notice's message ID: the review's, prefixed, or
// its digest when that would be too long.
func reviewNoticeID(reviewID string) string {
	if id := reviewNoticePrefix + reviewID; len(id) <= 128 {
		return id
	}
	sum := sha256.Sum256([]byte(reviewID))
	return reviewNoticePrefix + hex.EncodeToString(sum[:])
}

// reviewNoticeText says what was asked and what to call.
func reviewNoticeText(msg protocol.Message) string {
	spec := msg.ReviewSpec
	text := fmt.Sprintf("Review requested %s: %s#%s", msg.MessageID, spec.Repository, spec.Change)
	if spec.Scope != "" {
		text += ", scope: " + spec.Scope
	}
	return text + ". " + reviewPolicyText(spec.Policy) + " Call review_requests, then accept_review or decline_review."
}

func reviewPolicyText(p *protocol.ReviewPolicy) string {
	switch {
	case p == nil || p.Publish && p.Formal:
		return "Publish as a formal review."
	case p.Publish:
		return "Publish as a comment."
	}
	return "Don't publish."
}

// deliverReviewNotice follows a stored notice: it takes its sequence, tells
// the operator a message was sent, and pushes it to a connected reviewer;
// one not connected gets it on replay.
func (h *Hub) deliverReviewNotice(clients map[string]*Client, notice *store.ReviewNotice) {
	if notice == nil {
		return
	}
	out := notice.Msg
	h.seq = out.Seq
	h.emit(protocol.SessionEvent{Kind: protocol.EventConversationChanged, Seq: out.Seq, MessageID: out.MessageID, Direction: "sent"})
	for _, r := range notice.Recipients {
		if c := sessionOwner(clients, r); c != nil {
			h.enqueue(clients, c, out)
		}
	}
}

// reviewStoreError is what the operator hears of a request or cancel the
// store refused: a review outcome, a reviewer's full inbox, or an internal
// failure whose detail stays in the log.
func (h *Hub) reviewStoreError(id string, err error) *routingError {
	if code, ok := reviewCode(err); ok {
		return &routingError{code, err.Error()}
	}
	if errors.Is(err, store.ErrInboxFull) || errors.Is(err, store.ErrDuplicateMessage) {
		return h.persistenceError(id, err)
	}
	h.log.Error("review not stored", "message_id", id, "error", err)
	return &routingError{protocol.ErrInternal, "review not stored"}
}
