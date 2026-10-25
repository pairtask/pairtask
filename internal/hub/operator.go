package hub

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

type operatorRequestKind int

const (
	reqOneShot operatorRequestKind = iota
	reqOpen
	reqSession
	reqClose
)

type operatorRequest struct {
	ctx   context.Context
	uid   int
	msg   protocol.Message
	reply chan protocol.Message
	kind  operatorRequestKind
	sess  *OperatorSession
}

// SubmitOperator routes one request from an authenticated operator connection
// and returns its response frame. The caller has already verified the peer UID.
func (h *Hub) SubmitOperator(ctx context.Context, uid int, msg protocol.Message) protocol.Message {
	return h.submit(ctx, operatorRequest{ctx: ctx, uid: uid, msg: msg})
}

// submit hands a request to the hub and waits for its response.
func (h *Hub) submit(ctx context.Context, req operatorRequest) protocol.Message {
	req.reply = make(chan protocol.Message, 1)
	unavailable := protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "broker is shutting down", MessageID: req.msg.MessageID, RequestID: req.msg.RequestID}
	if !h.enqueueOperator(ctx, req) {
		return unavailable
	}
	select {
	case <-h.done:
		return unavailable
	case out := <-req.reply:
		return out
	}
}

// AuditConnection records an operator connection that never produced a routed
// request: a denied peer, or an authorized peer whose frame could not be read.
// The detail is a fixed, payload-free reason.
func (h *Hub) AuditConnection(ctx context.Context, uid int, result, detail string) error {
	_, err := h.store.RecordOperatorAudit(ctx, store.OperatorAudit{Action: "connect", PrincipalUID: uid, Result: result, Detail: detail})
	return err
}

// operatorSend is one validated operator request with its audit intent row.
type operatorSend struct {
	msg     protocol.Message
	scope   store.OperatorScope
	auditID int64
}

// operatorHandler serves one operator request type.
type operatorHandler func(h *Hub, ctx context.Context, clients map[string]*Client, req operatorRequest) protocol.Message

// operatorHandlers route a request by its type; session-only types are
// refused here, since they need an open operator session.
var operatorHandlers = map[string]operatorHandler{
	protocol.TypeOperatorSend:     (*Hub).routeOperatorSend,
	protocol.TypeOperatorReceived: (*Hub).operatorReceived,
	protocol.TypeOperatorAck:      (*Hub).operatorAck,
	protocol.TypeOperatorAckMany:  (*Hub).operatorAckMany,
	protocol.TypeOperatorReceive: func(h *Hub, ctx context.Context, _ map[string]*Client, req operatorRequest) protocol.Message {
		return h.operatorRead(ctx, req, "receive", h.readInbox)
	},
	protocol.TypeOperatorHistory: func(h *Hub, ctx context.Context, _ map[string]*Client, req operatorRequest) protocol.Message {
		return h.operatorRead(ctx, req, "history", h.readHistory)
	},
	protocol.TypeOperatorInboxStatus: func(h *Hub, ctx context.Context, _ map[string]*Client, req operatorRequest) protocol.Message {
		return h.operatorInboxStatus(ctx, req)
	},
	protocol.TypeOperatorMessageStages: func(h *Hub, ctx context.Context, _ map[string]*Client, req operatorRequest) protocol.Message {
		return h.messageStages(ctx, req)
	},
}

func (h *Hub) routeOperator(ctx context.Context, clients map[string]*Client, req operatorRequest) protocol.Message {
	if handle := operatorHandlers[req.msg.Type]; handle != nil {
		return handle(h, ctx, clients, req)
	}
	rerr := &routingError{protocol.ErrUnsupportedOperation, "unsupported operator request type"}
	if req.msg.Type == protocol.TypeOperatorSessionOpen || req.msg.Type == protocol.TypeOperatorSnapshot {
		rerr = &routingError{protocol.ErrInvalidState, "this request needs an open operator session"}
	}
	h.auditRejected(ctx, "request", req, rerr)
	return rerr.frame(req.msg.MessageID)
}

func (h *Hub) routeOperatorSend(ctx context.Context, clients map[string]*Client, req operatorRequest) protocol.Message {
	msg := req.msg
	scope, rerr := operatorScope(msg)
	if rerr != nil {
		h.auditRejected(ctx, "send", req, rerr)
		return rerr.frame(msg.MessageID)
	}
	auditID, err := h.store.RecordOperatorAudit(ctx, store.OperatorAudit{Action: "send", PrincipalUID: req.uid, MessageID: msg.MessageID, Scope: scope.Kind, Result: store.AuditPending})
	if err != nil {
		h.log.Error("operator audit intent failed; request not routed", "message_id", msg.MessageID, "error", err)
		return (&routingError{protocol.ErrInternal, "operator audit unavailable; message not routed"}).frame(msg.MessageID)
	}
	out, rerr := h.admitOperator(ctx, clients, operatorSend{msg, scope, auditID})
	if rerr != nil {
		h.finishAudit(ctx, auditID, store.AuditOutcome{Result: store.AuditRejected, Detail: rerr.code})
		return rerr.frame(msg.MessageID)
	}
	return out
}

func (h *Hub) admitOperator(ctx context.Context, clients map[string]*Client, send operatorSend) (protocol.Message, *routingError) {
	retry, err := h.store.LookupOperatorRetry(ctx, send.msg, send.scope)
	if err != nil {
		return protocol.Message{}, h.persistenceError(send.msg.MessageID, err)
	}
	if retry != nil {
		return h.repeatOperator(ctx, send, retry)
	}
	return h.commitOperator(ctx, clients, send)
}

// repeatOperator answers an identical retry with the retained outcome; nothing is rerouted.
func (h *Hub) repeatOperator(ctx context.Context, send operatorSend, retry *store.OperatorRetry) (protocol.Message, *routingError) {
	if err := h.store.FinishOperatorAudit(ctx, send.auditID, store.AuditOutcome{Result: store.AuditRepeated}); err != nil {
		h.log.Error("operator audit result failed", "message_id", send.msg.MessageID, "error", err)
		return protocol.Message{}, &routingError{protocol.ErrInternal, "operator audit unavailable"}
	}
	return protocol.Message{Type: protocol.TypeAck, MessageID: send.msg.MessageID, Stage: protocol.StageAccepted, Seq: retry.Seq,
		Durable: true, Repeated: true, Recipients: retry.Recipients, Receipts: retry.Receipts, Excluded: retry.Excluded}, nil
}

// commitOperator freezes recipients, persists with the audit result, then delivers.
func (h *Hub) commitOperator(ctx context.Context, clients map[string]*Client, send operatorSend) (protocol.Message, *routingError) {
	if rerr := checkOperatorPayload(send.msg.Payload); rerr != nil {
		return protocol.Message{}, rerr
	}
	out := operatorFrame(send, h.seq+1)
	recipients, excluded, rerr := h.operatorRecipients(ctx, clients, out, send.scope)
	if rerr == nil {
		rerr = checkFrameSize(out)
	}
	if rerr != nil {
		return protocol.Message{}, rerr
	}
	h.seq++
	err := h.store.PersistOperatorMessage(ctx, store.OperatorMessage{Msg: out, Scope: send.scope, Recipients: recipients, Excluded: excluded, AuditID: send.auditID})
	if err != nil {
		return protocol.Message{}, h.persistenceError(out.MessageID, err)
	}
	h.emit(protocol.SessionEvent{Kind: protocol.EventConversationChanged, Seq: out.Seq, MessageID: out.MessageID, Direction: "sent"})
	for _, r := range recipients {
		if c := sessionOwner(clients, r); c != nil {
			h.enqueue(clients, c, out) // An undelivered durable frame stays pending for replay.
		}
	}
	return protocol.Message{Type: protocol.TypeAck, MessageID: out.MessageID, Stage: protocol.StageAccepted, Seq: out.Seq,
		Durable: true, Recipients: recipients, Excluded: excluded}, nil
}

func operatorFrame(send operatorSend, seq uint64) protocol.Message {
	ts := time.Now().UTC()
	return protocol.Message{
		Type: protocol.TypeMsg, Seq: seq, TS: &ts, From: protocol.OperatorID, To: operatorTo(send.msg, send.scope),
		Origin: protocol.OriginOperator, Payload: send.msg.Payload, MessageID: send.msg.MessageID, InReplyTo: send.msg.InReplyTo,
		AckRequested: true, Durable: true, Group: groupMembers(send.scope),
	}
}

// groupMembers is a group send's canonical member list, so receivers never
// infer a group from to; nil for direct and everyone sends.
func groupMembers(scope store.OperatorScope) []string {
	if scope.Kind != store.ScopeGroup {
		return nil
	}
	return scope.Members
}

func (h *Hub) auditRejected(ctx context.Context, action string, req operatorRequest, rerr *routingError) {
	_, err := h.store.RecordOperatorAudit(ctx, store.OperatorAudit{Action: action, PrincipalUID: req.uid, MessageID: req.msg.MessageID, Result: store.AuditRejected, Detail: rerr.code})
	if err != nil {
		h.log.Error("operator audit failed for rejected request", "error", err)
	}
}

func (h *Hub) finishAudit(ctx context.Context, id int64, outcome store.AuditOutcome) {
	if err := h.store.FinishOperatorAudit(ctx, id, outcome); err != nil {
		h.log.Error("operator audit result failed", "audit_id", id, "error", err)
	}
}

// operatorScope validates the request shape and canonicalizes its scope.
func operatorScope(msg protocol.Message) (store.OperatorScope, *routingError) {
	if detail := operatorEnvelopeError(msg); detail != "" {
		return store.OperatorScope{}, &routingError{protocol.ErrMalformedFrame, detail}
	}
	if msg.To == protocol.Broadcast {
		return store.OperatorScope{Kind: store.ScopeAll}, nil
	}
	scope := store.OperatorScope{Kind: store.ScopeGroup, Members: msg.Group}
	if msg.To != "" {
		scope = store.OperatorScope{Kind: store.ScopeDirect, Members: []string{msg.To}}
	}
	members, detail := canonicalMembers(scope.Members)
	if detail != "" {
		return store.OperatorScope{}, &routingError{protocol.ErrMalformedFrame, detail}
	}
	scope.Members = members
	return scope, nil
}

func operatorEnvelopeError(msg protocol.Message) string {
	switch {
	case msg.Type != protocol.TypeOperatorSend:
		return "expected operator_send"
	case msg.MessageID == "" || len(msg.MessageID) > 128 || len(msg.InReplyTo) > 128:
		return "message_id is required; message_id and in_reply_to must be at most 128 bytes"
	case msg.Origin != "" || len(msg.Excluded) != 0:
		return "origin and excluded are set by the broker"
	case (msg.To == "") == (len(msg.Group) == 0):
		return "exactly one of to or group is required"
	}
	return ""
}

// canonicalMembers sorts and deduplicates, so equivalent groups retry as one request.
func canonicalMembers(ids []string) ([]string, string) {
	members := slices.Clone(ids)
	sort.Strings(members)
	members = slices.Compact(members)
	if len(members) > protocol.MaxRecipients {
		return nil, "group exceeds 256 recipients"
	}
	for _, id := range members {
		if err := protocol.ValidateAgentID(id); err != nil {
			return nil, err.Error()
		}
	}
	return members, ""
}

// operatorTo is informational; operator_requests holds the canonical scope.
func operatorTo(msg protocol.Message, scope store.OperatorScope) string {
	if scope.Kind == store.ScopeGroup {
		return "group:" + msg.MessageID
	}
	return msg.To
}

func (h *Hub) operatorRecipients(ctx context.Context, clients map[string]*Client, out protocol.Message, scope store.OperatorScope) ([]protocol.Recipient, []string, *routingError) {
	if scope.Kind == store.ScopeAll {
		return h.operatorBroadcast(ctx, clients, out)
	}
	recipients := make([]protocol.Recipient, 0, len(scope.Members))
	for _, id := range scope.Members {
		r, rerr := h.operatorMember(ctx, clients, id)
		if rerr != nil {
			return nil, nil, rerr
		}
		recipients = append(recipients, r)
	}
	return recipients, nil, nil
}

// operatorMember admits a connected v4 agent, or an offline one whose last
// durable registration negotiated v4, so it can display the origin on replay.
func (h *Hub) operatorMember(ctx context.Context, clients map[string]*Client, id string) (protocol.Recipient, *routingError) {
	if c := clients[id]; c != nil {
		if c.ProtocolVersion < protocol.OriginVersion {
			return protocol.Recipient{}, &routingError{protocol.ErrRecipientUnavailable, "recipient " + id + " must upgrade to protocol_version 4 to receive operator messages"}
		}
		return c.identity(), nil
	}
	version, err := h.store.AgentProtocolVersion(ctx, id)
	if err != nil {
		return protocol.Recipient{}, &routingError{protocol.ErrInternal, "cannot determine recipient capability"}
	}
	if version < protocol.OriginVersion {
		return protocol.Recipient{}, &routingError{protocol.ErrUnknownRecipient, "no eligible recipient with id " + id + "; offline operator delivery requires a prior protocol_version 4 registration"}
	}
	return protocol.Recipient{AgentID: id}, nil
}

// operatorBroadcast freezes connected eligible agents in ID order. Agents below
// v4 or with a full inbox are excluded; global limits then apply to all or none.
func (h *Hub) operatorBroadcast(ctx context.Context, clients map[string]*Client, out protocol.Message) ([]protocol.Recipient, []string, *routingError) {
	wire, err := store.DurableWireBytes(out)
	if err != nil {
		return nil, nil, &routingError{protocol.ErrMalformedFrame, "payload is not valid JSON"}
	}
	recipients, excluded, rerr := h.partitionBroadcast(ctx, sortedClients(clients), wire)
	if rerr == nil {
		rerr = checkBroadcastSize(recipients)
	}
	if rerr != nil {
		return nil, nil, rerr
	}
	return recipients, excluded, nil
}

func sortedClients(clients map[string]*Client) []*Client {
	sorted := make([]*Client, 0, len(clients))
	for _, c := range clients {
		sorted = append(sorted, c)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	return sorted
}

func (h *Hub) partitionBroadcast(ctx context.Context, candidates []*Client, wire int) ([]protocol.Recipient, []string, *routingError) {
	recipients := []protocol.Recipient{}
	excluded := []string{}
	for _, c := range candidates {
		eligible, rerr := h.broadcastEligible(ctx, c, wire)
		if rerr != nil {
			return nil, nil, rerr
		}
		if eligible {
			recipients = append(recipients, c.identity())
		} else {
			excluded = append(excluded, c.ID)
		}
	}
	return recipients, excluded, nil
}

func checkBroadcastSize(recipients []protocol.Recipient) *routingError {
	if len(recipients) == 0 {
		return &routingError{protocol.ErrRecipientUnavailable, "no eligible recipients"}
	}
	if len(recipients) > protocol.MaxRecipients {
		return &routingError{protocol.ErrMalformedFrame, "too many recipients; send to smaller groups"}
	}
	return nil
}

// broadcastEligible admits a connected agent that can display origin and whose
// own inbox has room; global limits are checked later for the whole set.
func (h *Hub) broadcastEligible(ctx context.Context, c *Client, wire int) (bool, *routingError) {
	if c.ProtocolVersion < protocol.OriginVersion {
		return false, nil
	}
	fits, err := h.store.RecipientCapacity(ctx, c.ID, wire)
	if err != nil {
		return false, &routingError{protocol.ErrInternal, "cannot determine recipient capacity"}
	}
	return fits, nil
}

func checkFrameSize(out protocol.Message) *routingError {
	sized := out
	sized.Replayed = true
	encoded, err := json.Marshal(sized)
	if err != nil {
		return &routingError{protocol.ErrMalformedFrame, "payload is not valid JSON"}
	}
	if len(encoded)+1 > protocol.MaxFrameBytes {
		return &routingError{protocol.ErrMalformedFrame, "routed message exceeds the 1 MiB frame limit"}
	}
	return nil
}

// checkOperatorPayload caps new operator sends on their encoded wire form, so
// every new history entry fits a page. Exact retries resolve before this check.
func checkOperatorPayload(payload json.RawMessage) *routingError {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return &routingError{protocol.ErrMalformedFrame, "payload is not valid JSON"}
	}
	if len(encoded) > protocol.OperatorPayloadMax {
		return &routingError{protocol.ErrMalformedFrame, "payload too large; operator messages are limited to 512 KiB"}
	}
	return nil
}
