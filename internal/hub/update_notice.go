package hub

import (
	"context"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// operatorUpdateNotice keeps the host's update report for the chat. It lives
// in memory only: a broker restart, which every rollout is, starts without
// one, so a report about the previous app never outlives it. A report
// observed before the kept one is refused: a slow refresh that finishes
// after a newer check never puts the older state back.
func (h *Hub) operatorUpdateNotice(ctx context.Context, _ map[string]*Client, req operatorRequest) protocol.Message {
	msg := req.msg
	if msg.UpdateNotice == nil || msg.UpdateNotice.Validate(time.Now()) != nil {
		rerr := &routingError{protocol.ErrMalformedFrame, "update_notice needs a known state and action, release tags, a commit and past times"}
		h.auditRejected(ctx, "update notice", req, rerr)
		return rerr.frame(msg.MessageID)
	}
	notice := *msg.UpdateNotice
	if h.updateNotice != nil && notice.ObservedBefore(*h.updateNotice) {
		rerr := &routingError{protocol.ErrInvalidState, "a newer update notice is already kept"}
		h.auditRejected(ctx, "update notice", req, rerr)
		return rerr.frame(msg.MessageID)
	}
	if _, err := h.store.RecordOperatorAudit(ctx, store.OperatorAudit{Action: "update notice", PrincipalUID: req.uid,
		MessageID: msg.MessageID, Result: store.AuditAccepted}); err != nil {
		return (&routingError{protocol.ErrInternal, "operator audit unavailable; notice not kept"}).frame(msg.MessageID)
	}
	h.updateNotice = &notice
	h.emit(protocol.SessionEvent{Kind: protocol.EventUpdateNoticeChanged})
	return protocol.Message{Type: protocol.TypeOperatorUpdateNoticeResult, MessageID: msg.MessageID, UpdateNotice: &notice}
}

// snapshotUpdateNotice is a copy of the kept notice, or nil.
func (h *Hub) snapshotUpdateNotice() *protocol.UpdateNotice {
	if h.updateNotice == nil {
		return nil
	}
	notice := *h.updateNotice
	return &notice
}
