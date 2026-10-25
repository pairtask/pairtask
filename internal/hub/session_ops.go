package hub

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// snapshotReserve keeps a snapshot frame below the wire limit after framing.
const snapshotReserve = 4 << 10

// snapshotStages bounds recipient stages per recent message in a snapshot;
// larger fan-outs are paged with operator_message_stages.
const snapshotStages = 64

// OpenOperatorSession claims the operator inbox for sess, or reports owner_busy.
func (h *Hub) OpenOperatorSession(ctx context.Context, msg protocol.Message, sess *OperatorSession) protocol.Message {
	return h.submit(ctx, operatorRequest{ctx: ctx, uid: sess.UID, msg: msg, kind: reqOpen, sess: sess})
}

// SessionRequest routes a request from an open session; its response is
// queued on the session, never returned here.
func (h *Hub) SessionRequest(ctx context.Context, sess *OperatorSession, msg protocol.Message) bool {
	return h.enqueueOperator(ctx, operatorRequest{ctx: ctx, uid: sess.UID, msg: msg, kind: reqSession, sess: sess})
}

// CloseOperatorSession releases ownership if sess still holds it.
func (h *Hub) CloseOperatorSession(sess *OperatorSession) {
	h.enqueueOperator(context.Background(), operatorRequest{ctx: context.Background(), uid: sess.UID, kind: reqClose, sess: sess})
}

func (h *Hub) enqueueOperator(ctx context.Context, req operatorRequest) bool {
	select {
	case <-h.done:
		return false
	case <-ctx.Done():
		return false
	case h.operator <- req:
		return true
	}
}

func (h *Hub) handleOperator(ctx context.Context, clients map[string]*Client, req operatorRequest) {
	switch req.kind {
	case reqOpen:
		req.reply <- h.openSession(ctx, req)
	case reqSession:
		h.routeSession(ctx, clients, req)
	case reqClose:
		h.closeSession(ctx, req.sess)
	default:
		req.reply <- h.routeOperator(ctx, clients, req)
	}
}

func (h *Hub) openSession(ctx context.Context, req operatorRequest) protocol.Message {
	if h.owner != nil {
		return protocol.Message{Type: protocol.TypeError, Code: protocol.ErrOwnerBusy, RequestID: req.msg.RequestID,
			Detail: "another operator session owns the inbox; close it first",
			Owner:  &protocol.SessionInfo{SessionID: h.owner.ID, OpenedAt: h.owner.OpenedAt}}
	}
	// Without an audit row there is no ownership.
	if err := h.store.RecordSessionAudit(ctx, "session_open", req.uid, req.sess.ID); err != nil {
		h.log.Error("operator session audit failed; session not opened", "error", err)
		return protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, RequestID: req.msg.RequestID, Detail: "operator audit unavailable"}
	}
	h.gen++
	req.sess.gen = h.gen
	h.owner = req.sess
	return protocol.Message{Type: protocol.TypeOperatorSession, RequestID: req.msg.RequestID, Session: &protocol.SessionInfo{
		SessionID: req.sess.ID, Epoch: h.epoch, Capabilities: protocol.SessionCapabilities, OpenedAt: req.sess.OpenedAt}}
}

// closeSession releases only the matching generation, never a newer owner.
// Closing always releases; an audit failure is logged.
func (h *Hub) closeSession(ctx context.Context, sess *OperatorSession) {
	if h.owner != sess || h.owner.gen != sess.gen {
		return
	}
	h.owner = nil
	if err := h.store.RecordSessionAudit(ctx, "session_close", sess.UID, sess.ID); err != nil {
		h.log.Error("operator session close audit failed; ownership released", "error", err)
	}
}

func (h *Hub) routeSession(ctx context.Context, clients map[string]*Client, req operatorRequest) {
	var resp protocol.Message
	switch req.msg.Type {
	case protocol.TypeOperatorSnapshot:
		h.sessionSnapshot(ctx, clients, req)
		return
	case protocol.TypeOperatorMessageStages:
		resp = h.messageStages(ctx, req)
	case protocol.TypeOperatorSessionOpen:
		resp = protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInvalidState, Detail: "this session is already open"}
	default:
		resp = h.routeOperator(ctx, clients, req)
	}
	resp.RequestID = req.msg.RequestID
	req.sess.pushResponse(resp)
}

// ownerBusy refuses one-shot inbox writes while a live session owns the inbox.
func (h *Hub) ownerBusy(req operatorRequest) *protocol.Message {
	if req.sess != nil || h.owner == nil {
		return nil
	}
	return &protocol.Message{Type: protocol.TypeError, Code: protocol.ErrOwnerBusy, MessageID: req.msg.MessageID,
		Detail: "a live operator session owns the inbox; acknowledge from that session",
		Owner:  &protocol.SessionInfo{SessionID: h.owner.ID, OpenedAt: h.owner.OpenedAt}}
}

// sessionSnapshot cuts the baseline and queues the response in one hub step.
func (h *Hub) sessionSnapshot(ctx context.Context, clients map[string]*Client, req operatorRequest) {
	snap, err := h.buildSnapshot(ctx, clients)
	if err != nil {
		h.log.Error("operator snapshot failed", "error", err)
		req.sess.pushResponse(protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, RequestID: req.msg.RequestID, Detail: "snapshot unavailable"})
		return
	}
	req.sess.cut(protocol.Message{Type: protocol.TypeOperatorSnapshot, RequestID: req.msg.RequestID, Snapshot: &snap})
}

func (h *Hub) buildSnapshot(ctx context.Context, clients map[string]*Client) (protocol.OperatorSnapshot, error) {
	snap := protocol.OperatorSnapshot{Watermark: h.watermark()}
	snap.Agents, snap.AgentsTotal = presence(clients)
	snap.AgentsTruncated = snap.AgentsTotal > len(snap.Agents)
	var err error
	if snap.InboxHeadSeq, snap.InboxPending, err = h.store.OperatorInboxState(ctx); err != nil {
		return snap, err
	}
	sent, err := h.store.RecentOperatorSent(ctx, store.SentQuery{Limit: protocol.SnapshotRecentSent + 1, StageLimit: snapshotStages})
	if err != nil {
		return snap, err
	}
	snap.RecentSentTruncated = len(sent) > protocol.SnapshotRecentSent
	snap.RecentSent = sent[:min(len(sent), protocol.SnapshotRecentSent)]
	if snap.AgentStatus, snap.AgentStatusTotal, err = h.snapshotStatuses(ctx, clients); err != nil {
		return snap, err
	}
	snap.AgentStatusTruncated = snap.AgentStatusTotal > len(snap.AgentStatus)
	return fitSnapshot(snap)
}

// fitSnapshot drops the oldest recent messages until the frame fits.
func fitSnapshot(snap protocol.OperatorSnapshot) (protocol.OperatorSnapshot, error) {
	for {
		data, err := json.Marshal(protocol.Message{Type: protocol.TypeOperatorSnapshot, RequestID: string(make([]byte, 128)), Snapshot: &snap})
		if err != nil {
			return snap, err
		}
		if len(data)+snapshotReserve <= protocol.MaxFrameBytes {
			return snap, nil
		}
		switch {
		case len(snap.RecentSent) > 0:
			snap.RecentSent = snap.RecentSent[:len(snap.RecentSent)-1]
			snap.RecentSentTruncated = true
		case len(snap.AgentStatus) > 0:
			snap.AgentStatus = snap.AgentStatus[:len(snap.AgentStatus)-1]
			snap.AgentStatusTruncated = true
		default:
			return snap, nil
		}
	}
}

func presence(clients map[string]*Client) ([]protocol.AgentPresence, int) {
	agents := make([]protocol.AgentPresence, 0, len(clients))
	for _, c := range clients {
		agents = append(agents, protocol.AgentPresence{AgentID: c.ID, SessionID: c.SessionID, ProtocolVersion: c.ProtocolVersion})
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].AgentID < agents[j].AgentID })
	return agents[:min(len(agents), protocol.SnapshotAgents)], len(agents)
}

func (h *Hub) messageStages(ctx context.Context, req operatorRequest) protocol.Message {
	limit, rerr := stagesLimit(req.msg)
	if rerr != nil {
		return rerr.frame(req.msg.MessageID)
	}
	page, err := h.store.MessageStages(ctx, store.StageQuery{MessageID: req.msg.MessageID, AfterAgent: req.msg.AfterAgent, Limit: limit})
	switch {
	case errors.Is(err, store.ErrInvalidMessage):
		return (&routingError{protocol.ErrInvalidMessage, err.Error()}).frame(req.msg.MessageID)
	case errors.Is(err, store.ErrInvalidCursor):
		return (&routingError{protocol.ErrMalformedFrame, "unknown after_agent"}).frame(req.msg.MessageID)
	case err != nil:
		h.log.Error("message stages read failed", "error", err)
		return (&routingError{protocol.ErrInternal, "stages unavailable"}).frame(req.msg.MessageID)
	}
	page.Watermark = h.watermark()
	return protocol.Message{Type: protocol.TypeOperatorMessageStages, Stages: &page}
}

func stagesLimit(msg protocol.Message) (int, *routingError) {
	if msg.MessageID == "" || len(msg.MessageID) > 128 {
		return 0, &routingError{protocol.ErrMalformedFrame, "message_id must be 1 to 128 bytes"}
	}
	if msg.Limit == 0 {
		return protocol.StagesDefault, nil
	}
	if msg.Limit < 0 || msg.Limit > protocol.StagesMax {
		return 0, &routingError{protocol.ErrMalformedFrame, "limit must be 1 to 256, or omitted for 64"}
	}
	return msg.Limit, nil
}

func (h *Hub) watermark() protocol.Watermark {
	return protocol.Watermark{Epoch: h.epoch, Revision: h.revision}
}

// emit advances the revision for every event and hands it to the owner, if any.
func (h *Hub) emit(ev protocol.SessionEvent) {
	h.revision++
	ev.Watermark = h.watermark()
	if h.owner != nil {
		h.owner.pushEvent(protocol.Message{Type: protocol.TypeEvent, Event: &ev})
	}
}

// emitInbox reports the inbox state. If it can't be read, the owner must
// resync rather than miss a change.
func (h *Hub) emitInbox(ctx context.Context) {
	head, pending, err := h.store.OperatorInboxState(ctx)
	if err != nil {
		h.log.Error("operator inbox state unavailable; forcing resync", "error", err)
		h.forceResync()
		return
	}
	h.emit(protocol.SessionEvent{Kind: protocol.EventInboxChanged, HeadSeq: head, Pending: &pending})
}

func (h *Hub) forceResync() {
	h.revision++
	if h.owner != nil {
		h.owner.forceResync(h.watermark())
	}
}

// noteRouted reports agent messages that reached the operator inbox.
func (h *Hub) noteRouted(ctx context.Context, out protocol.Message) {
	if out.To != protocol.OperatorID {
		return
	}
	h.emit(protocol.SessionEvent{Kind: protocol.EventConversationChanged, Seq: out.Seq, MessageID: out.MessageID, Direction: "received"})
	h.emitInbox(ctx)
}

// noteAck reports a committed first transition on an operator-sent message.
func (h *Hub) noteAck(in Inbound, report store.AckReport) {
	if !report.FirstOperator {
		return
	}
	h.emit(protocol.SessionEvent{Kind: protocol.EventStageChanged, MessageID: in.Msg.MessageID, AgentID: in.From.ID, SessionID: in.From.SessionID, Stage: in.Msg.Stage})
}

// noteConnected reports presence and operator-sent deliveries this claim
// moved to the new session, whose stage is accepted again.
func (h *Hub) noteConnected(c *Client, reclaimed []string) {
	h.notePresence(c, "connected")
	for _, id := range reclaimed {
		h.emit(protocol.SessionEvent{Kind: protocol.EventStageChanged, MessageID: id, AgentID: c.ID, SessionID: c.SessionID, Stage: protocol.StageAccepted})
	}
}

func (h *Hub) notePresence(c *Client, state string) {
	h.emit(protocol.SessionEvent{Kind: protocol.EventPresenceChanged, AgentID: c.ID, SessionID: c.SessionID, State: state, ProtocolVersion: c.ProtocolVersion})
}
