package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/review"
	"github.com/pairtask/pairtask/internal/store"
)

// Review requests (#201): the operator asks one agent to review one change;
// the agent's side pins the revisions, reviews, and reports its verdict and
// each publication step. Expiry is lazy: every review frame and operator
// review request first ends what has run out of time, so nothing reads or
// changes a review past its time, and the operator hears of it then.

// reviewFrames are the agent's review requests (v10).
var reviewFrames = map[string]bool{
	protocol.TypeReviewGet: true, protocol.TypeReviewPrepare: true, protocol.TypeReviewAccept: true,
	protocol.TypeReviewDecline: true, protocol.TypeReviewRestart: true, protocol.TypeReviewSubmit: true,
	protocol.TypeReviewPublication: true, protocol.TypeReviewObserve: true,
}

// reviewErrors maps lifecycle outcomes to the answer an agent or the
// operator sees.
var reviewErrors = []struct {
	err  error
	code string
}{
	{review.ErrNotFound, protocol.ErrInvalidReview},
	{review.ErrIDReused, protocol.ErrReviewConflict},
	{review.ErrFull, protocol.ErrInvalidReview},
	{review.ErrLimit, protocol.ErrInvalidReview},
	{review.ErrConflict, protocol.ErrReviewConflict},
}

// SetReviewClock sets the clock of review times; call it before Run.
func (h *Hub) SetReviewClock(clock func() time.Time) { h.reviewClock = clock }

func (h *Hub) reviewNow() time.Time {
	if h.reviewClock != nil {
		return h.reviewClock().UTC()
	}
	return time.Now().UTC()
}

// expireReviews ends every review whose time is up and tells the operator.
func (h *Hub) expireReviews(ctx context.Context) error {
	expired, err := h.store.ExpireReviews(ctx, h.reviewNow())
	for _, id := range expired {
		h.emit(protocol.SessionEvent{Kind: protocol.EventReviewsChanged, ReviewID: id})
	}
	return err
}

func (h *Hub) handleReview(ctx context.Context, clients map[string]*Client, in Inbound) {
	h.enqueue(clients, in.From, h.agentReview(ctx, in))
}

// agentReview serves one review frame from the reviewer's side. Only the
// named reviewer sees or changes a review; to anyone else it doesn't exist.
func (h *Hub) agentReview(ctx context.Context, in Inbound) protocol.Message {
	msg := in.Msg
	if refusal := h.reviewPreflight(in); refusal != nil {
		return *refusal
	}
	if err := h.expireReviews(ctx); err != nil {
		return h.reviewFailure(msg.RequestID, err)
	}
	if msg.Type == protocol.TypeReviewGet {
		return h.reviewGet(ctx, in)
	}
	var step review.Step
	now := h.reviewNow()
	act := store.ReviewAction{Actor: in.From.ID, Action: msg.Type}
	rec, changed, err := h.store.ChangeReview(ctx, msg.ReviewID, act, func(r review.Record) (review.Record, bool, error) {
		if r.View.Spec.Reviewer != in.From.ID {
			return r, false, review.ErrNotFound
		}
		next, s, err := agentStep(r, msg, now)
		step = s
		return next, s.Changed, err
	})
	if err != nil {
		return h.reviewFailure(msg.RequestID, err)
	}
	if changed {
		h.emit(protocol.SessionEvent{Kind: protocol.EventReviewsChanged, ReviewID: msg.ReviewID})
	}
	view := rec.View
	answer := protocol.Message{Type: protocol.TypeReviewResult, RequestID: msg.RequestID, ReviewID: msg.ReviewID, Review: &view}
	if msg.Type == protocol.TypeReviewPublication {
		answer.IntentAcquired = &step.Acquired // false is an answer too: never write on it
	}
	return answer
}

// agentStep applies one agent frame to its review.
func agentStep(r review.Record, msg protocol.Message, now time.Time) (review.Record, review.Step, error) {
	var changed bool
	var err error
	switch msg.Type {
	case protocol.TypeReviewPrepare:
		r, changed, err = review.Prepare(r, *msg.Revisions, now)
	case protocol.TypeReviewAccept:
		r, changed, err = review.Accept(r, now)
	case protocol.TypeReviewDecline:
		r, changed, err = review.Decline(r, msg.ReviewReason, now)
	case protocol.TypeReviewRestart:
		r, changed, err = review.Restart(r, msg.Attempt, now)
	case protocol.TypeReviewSubmit:
		r, changed, err = review.Submit(r, msg.Attempt, *msg.Verdict, now)
	case protocol.TypeReviewPublication:
		return review.Publish(r, msg.Attempt, *msg.PublicationStep, now)
	case protocol.TypeReviewObserve:
		r, changed, err = review.Observe(r, msg.Attempt, *msg.Revisions, now)
	}
	return r, review.Step{Changed: changed}, err
}

// reviewGet answers one review of this agent's, or all of them.
func (h *Hub) reviewGet(ctx context.Context, in Inbound) protocol.Message {
	msg := in.Msg
	if msg.ReviewID != "" {
		rec, found, err := h.store.GetReview(ctx, msg.ReviewID)
		if err == nil && !ownReview(rec, found, in.From.ID) {
			err = review.ErrNotFound
		}
		if err != nil {
			return h.reviewFailure(msg.RequestID, err)
		}
		view := rec.View
		return protocol.Message{Type: protocol.TypeReviewResult, RequestID: msg.RequestID, ReviewID: msg.ReviewID, Review: &view}
	}
	page, err := h.reviewPage(ctx, in.From.ID)
	if err != nil {
		return h.reviewFailure(msg.RequestID, err)
	}
	return protocol.Message{Type: protocol.TypeReviewResult, RequestID: msg.RequestID, ReviewPage: page}
}

// ownReview: a stored review whose reviewer is agent.
func ownReview(rec review.Record, found bool, agent string) bool {
	return found && rec.View.Spec.Reviewer == agent
}

// reviewPage is the newest reviews, of one reviewer or all, with the total.
func (h *Hub) reviewPage(ctx context.Context, reviewer string) (*protocol.ReviewPage, error) {
	recs, total, err := h.store.Reviews(ctx, reviewer, protocol.ReviewListed)
	if err != nil {
		return nil, err
	}
	page := &protocol.ReviewPage{Reviews: make([]protocol.ReviewView, 0, len(recs)), Total: total}
	for _, r := range recs {
		page.Reviews = append(page.Reviews, r.View)
	}
	fitReviews(page)
	return page, nil
}

// reviewPageBytes leaves room in a frame for its envelope.
const reviewPageBytes = protocol.MaxFrameBytes - 16<<10

// fitReviews drops the oldest reviews until the page fits one frame; each
// one kept is whole, and one review alone always fits.
func fitReviews(page *protocol.ReviewPage) {
	for len(page.Reviews) > 1 {
		encoded, err := json.Marshal(page)
		if err == nil && len(encoded) <= reviewPageBytes {
			break
		}
		page.Reviews = page.Reviews[:len(page.Reviews)-1]
	}
	page.Truncated = page.Total > len(page.Reviews)
}

// reviewPreflight refuses a frame before the store sees it: a missing
// request_id, an old protocol, bad fields, or a spent change budget.
func (h *Hub) reviewPreflight(in Inbound) *protocol.Message {
	msg := in.Msg
	refuse := func(code, detail string) *protocol.Message {
		frame := statusError(msg.RequestID, code, detail)
		return &frame
	}
	switch {
	case msg.RequestID == "" || len(msg.RequestID) > 128:
		return refuse(protocol.ErrMalformedFrame, "request_id is required, at most 128 bytes")
	case in.From.ProtocolVersion < protocol.ReviewVersion:
		return refuse(protocol.ErrMalformedFrame, fmt.Sprintf("review requests require protocol_version %d", protocol.ReviewVersion))
	}
	if err := validReviewFrame(msg); err != nil {
		return refuse(protocol.ErrInvalidReview, err.Error())
	}
	if msg.Type != protocol.TypeReviewGet && !h.statuses.takeReview(in.From, time.Now()) {
		return refuse(protocol.ErrReviewRateLimited, "too many review changes; retry shortly")
	}
	return nil
}

// reviewFieldChecks validate what each agent frame carries beyond its ID.
var reviewFieldChecks = map[string]func(protocol.Message) error{
	protocol.TypeReviewPrepare: func(m protocol.Message) error { return protocol.ValidateRevisions(m.Revisions) },
	protocol.TypeReviewObserve: func(m protocol.Message) error {
		return errors.Join(validAttempt(m), protocol.ValidateRevisions(m.Revisions))
	},
	protocol.TypeReviewDecline: func(m protocol.Message) error { return protocol.ValidateReviewReason(m.ReviewReason) },
	protocol.TypeReviewRestart: validAttempt,
	protocol.TypeReviewSubmit: func(m protocol.Message) error {
		return errors.Join(validAttempt(m), protocol.ValidateVerdict(m.Verdict))
	},
	protocol.TypeReviewPublication: func(m protocol.Message) error {
		return errors.Join(validAttempt(m), protocol.ValidatePublicationUpdate(m.PublicationStep))
	},
}

func validAttempt(m protocol.Message) error {
	if m.Attempt < 1 {
		return errors.New("attempt must be at least 1")
	}
	return nil
}

func validReviewFrame(msg protocol.Message) error {
	if msg.Type == protocol.TypeReviewGet && msg.ReviewID == "" {
		return nil
	}
	if err := protocol.ValidateReviewID(msg.ReviewID); err != nil {
		return err
	}
	if check := reviewFieldChecks[msg.Type]; check != nil {
		return check(msg)
	}
	return nil
}

// reviewFailure answers a refused review frame.
func (h *Hub) reviewFailure(request string, err error) protocol.Message {
	if code, ok := reviewCode(err); ok {
		return statusError(request, code, err.Error())
	}
	h.log.Error("review not stored", "request_id", request, "error", err)
	return statusError(request, protocol.ErrInternal, "review not stored")
}

func reviewCode(err error) (string, bool) {
	for _, m := range reviewErrors {
		if errors.Is(err, m.err) {
			return m.code, true
		}
	}
	return "", false
}

// operatorReview serves operator_review: a request or a cancellation, each
// audited before anything changes, or a read.
func (h *Hub) operatorReview(ctx context.Context, clients map[string]*Client, req operatorRequest) protocol.Message {
	msg := req.msg
	if len(msg.MessageID) > 128 {
		return (&routingError{protocol.ErrMalformedFrame, "message_id must be at most 128 bytes"}).frame("")
	}
	if err := h.expireReviews(ctx); err != nil {
		return (&routingError{protocol.ErrInternal, "reviews unavailable"}).frame(msg.MessageID)
	}
	switch msg.ReviewOp {
	case protocol.ReviewOpRequest, protocol.ReviewOpCancel:
		return h.operatorReviewChange(ctx, clients, req)
	case protocol.ReviewOpList:
		page, err := h.reviewPage(ctx, "")
		if err != nil {
			return (&routingError{protocol.ErrInternal, "reviews unavailable"}).frame(msg.MessageID)
		}
		return protocol.Message{Type: protocol.TypeOperatorReviewResult, MessageID: msg.MessageID, ReviewPage: page}
	case protocol.ReviewOpStatus:
		return h.operatorReviewStatus(ctx, msg)
	}
	return (&routingError{protocol.ErrMalformedFrame, "review_op must be request, cancel, list or status"}).frame(msg.MessageID)
}

func (h *Hub) operatorReviewStatus(ctx context.Context, msg protocol.Message) protocol.Message {
	if err := protocol.ValidateReviewID(msg.ReviewID); err != nil {
		return (&routingError{protocol.ErrMalformedFrame, err.Error()}).frame(msg.MessageID)
	}
	rec, found, err := h.store.GetReview(ctx, msg.ReviewID)
	switch {
	case err != nil:
		return (&routingError{protocol.ErrInternal, "reviews unavailable"}).frame(msg.MessageID)
	case !found:
		return (&routingError{protocol.ErrInvalidReview, "no such review"}).frame(msg.MessageID)
	}
	view := rec.View
	return protocol.Message{Type: protocol.TypeOperatorReviewResult, MessageID: msg.MessageID, ReviewID: view.ID, Review: &view}
}

// operatorReviewChange checks, audits and applies a request or a
// cancellation; nothing changes without its audit row.
func (h *Hub) operatorReviewChange(ctx context.Context, clients map[string]*Client, req operatorRequest) protocol.Message {
	msg := req.msg
	if rerr := checkOperatorReview(msg); rerr != nil {
		return rerr.frame(msg.MessageID)
	}
	notice, rerr := h.reviewNotice(ctx, clients, msg)
	if rerr != nil {
		h.auditRejected(ctx, "review "+msg.ReviewOp, req, rerr)
		return rerr.frame(msg.MessageID)
	}
	scope := msg.ReviewID
	if msg.ReviewOp == protocol.ReviewOpRequest {
		scope = msg.MessageID
	}
	auditID, err := h.store.RecordOperatorAudit(ctx, store.OperatorAudit{Action: "review " + msg.ReviewOp, PrincipalUID: req.uid,
		MessageID: msg.MessageID, Scope: scope, Result: store.AuditPending})
	if err != nil {
		return (&routingError{protocol.ErrInternal, "operator audit unavailable; reviews unchanged"}).frame(msg.MessageID)
	}
	rec, changed, err := h.applyOperatorReview(ctx, req, store.ReviewAction{AuditID: auditID, Notice: notice})
	if err != nil {
		rerr := h.reviewStoreError(msg.MessageID, err)
		h.finishAudit(ctx, auditID, store.AuditOutcome{Result: store.AuditRejected, Detail: rerr.code})
		return rerr.frame(msg.MessageID)
	}
	if changed {
		h.emit(protocol.SessionEvent{Kind: protocol.EventReviewsChanged, ReviewID: rec.View.ID})
		h.deliverReviewNotice(clients, notice)
	}
	view := rec.View
	return protocol.Message{Type: protocol.TypeOperatorReviewResult, MessageID: msg.MessageID, ReviewID: view.ID, Review: &view}
}

// applyOperatorReview commits the change together with its audit row's
// accepted result.
func (h *Hub) applyOperatorReview(ctx context.Context, req operatorRequest, act store.ReviewAction) (review.Record, bool, error) {
	msg, now := req.msg, h.reviewNow()
	act.Actor, act.Action = fmt.Sprintf("operator:%d", req.uid), "operator "+msg.ReviewOp
	if msg.ReviewOp == protocol.ReviewOpRequest {
		return h.store.CreateReview(ctx, review.New(msg.MessageID, act.Actor, *msg.ReviewSpec, now), act)
	}
	return h.store.ChangeReview(ctx, msg.ReviewID, act, func(r review.Record) (review.Record, bool, error) {
		return review.Cancel(r, msg.ReviewReason, now)
	})
}

// checkOperatorReview checks a request's or a cancellation's fields before
// anything is audited.
func checkOperatorReview(msg protocol.Message) *routingError {
	if msg.ReviewOp == protocol.ReviewOpCancel {
		if err := errors.Join(protocol.ValidateReviewID(msg.ReviewID), protocol.ValidateReviewReason(msg.ReviewReason)); err != nil {
			return &routingError{protocol.ErrMalformedFrame, err.Error()}
		}
		return nil
	}
	if err := protocol.ValidateReviewID(msg.MessageID); err != nil {
		return &routingError{protocol.ErrMalformedFrame, "message_id names the request: " + err.Error()}
	}
	if msg.ReviewSpec == nil {
		return &routingError{protocol.ErrMalformedFrame, "review_spec is required"}
	}
	if err := protocol.ValidateReviewSpec(*msg.ReviewSpec); err != nil {
		return &routingError{protocol.ErrInvalidReview, err.Error()}
	}
	return nil
}
