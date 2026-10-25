package hub

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func (h *Hub) connect(clients map[string]*Client, c *Client) {
	if owner := clients[c.ID]; owner != nil {
		rejectRegistration(c, protocol.Message{Code: protocol.ErrDuplicateID, OwnerSessionID: owner.SessionID,
			Detail: "agent id " + c.ID + " is owned by session " + owner.SessionID + "; disconnect that owner before retrying, or use a distinct agent_id"})
		return
	}
	if err := h.recordConnect(c); err != nil {
		rejectRegistration(c, protocol.Message{Code: protocol.ErrInternal, Detail: "session not persisted; registration rejected"})
		return
	}
	if err := h.joinHelloRooms(context.Background(), c); err != nil {
		h.log.Error("hello rooms not joined", "agent_id", c.ID, "error", err)
		h.recordDisconnect(c)
		rejectRegistration(c, protocol.Message{Code: protocol.ErrInternal, Detail: "rooms not joined; registration rejected"})
		return
	}
	pending, reclaimed, err := h.claimPending(c)
	if err != nil {
		h.log.Error("recover inbox failed", "agent_id", c.ID, "error", err)
		h.recordDisconnect(c)
		rejectRegistration(c, protocol.Message{Code: protocol.ErrInternal, Detail: "durable inbox recovery failed; registration rejected; retry after fixing storage"})
		return
	}
	clients[c.ID] = c
	h.seq++
	h.enqueue(clients, c, h.welcomeRooms(context.Background(), c, protocol.Message{Type: protocol.TypeWelcome, AgentID: c.ID, SessionID: c.SessionID, Seq: h.seq, ProtocolVersion: c.ProtocolVersion}))
	for _, msg := range pending {
		if !h.enqueue(clients, c, msg) {
			return
		}
	}
	h.noteConnected(c, reclaimed)
	h.log.Info("agent connected", "agent_id", c.ID, "session_id", c.SessionID, "replayed", len(pending), "online", len(clients))
}

func rejectRegistration(c *Client, frame protocol.Message) {
	frame.Type, frame.AgentID, frame.SessionID = protocol.TypeError, c.ID, c.SessionID
	c.Send <- frame
	close(c.Send)
}

func (h *Hub) claimPending(c *Client) ([]protocol.Message, []string, error) {
	if c.ProtocolVersion < protocol.DurableVersion {
		return nil, nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return h.store.ClaimPendingReport(ctx, c.identity(), c.ProtocolVersion)
}

func validateDurability(in Inbound) *routingError {
	if !in.Msg.Durable {
		return nil
	}
	if in.From.ProtocolVersion < protocol.DurableVersion {
		return &routingError{protocol.ErrDurabilityUnavailable, "durable delivery requires protocol_version 3"}
	}
	if !in.Msg.AckRequested {
		return &routingError{protocol.ErrMalformedFrame, "durable delivery requires ack_requested"}
	}
	if in.Msg.To != protocol.Broadcast && in.Msg.To != protocol.OperatorID {
		if err := protocol.ValidateAgentID(in.Msg.To); err != nil {
			return &routingError{protocol.ErrMalformedFrame, err.Error()}
		}
	}
	return nil
}

func (h *Hub) resolveRecipients(ctx context.Context, clients map[string]*Client, in Inbound) ([]protocol.Recipient, *routingError) {
	if in.Msg.To == protocol.OperatorID {
		return h.operatorInbox(ctx, in)
	}
	if isPublic(in.Msg.To, in.From.ProtocolVersion) {
		return h.publicBroadcast(ctx, clients, in)
	}
	if in.Msg.To == protocol.Broadcast {
		recipients, err := broadcastRecipients(clients, in.From, everyone)
		if err != nil {
			return nil, err
		}
		return recipients, checkRecipientVersions(clients, in.Msg, recipients)
	}
	if c := clients[in.Msg.To]; c != nil {
		recipients := []protocol.Recipient{c.identity()}
		return recipients, checkRecipientVersions(clients, in.Msg, recipients)
	}
	return h.offlineRecipient(ctx, in.Msg)
}

func checkRecipientVersions(clients map[string]*Client, msg protocol.Message, recipients []protocol.Recipient) *routingError {
	if !msg.Durable {
		return nil
	}
	for _, r := range recipients {
		if clients[r.AgentID].ProtocolVersion < protocol.DurableVersion {
			return &routingError{protocol.ErrDurabilityUnavailable, "recipient " + r.AgentID + " must upgrade to protocol_version 3; message not accepted"}
		}
	}
	return nil
}

func (h *Hub) offlineRecipient(ctx context.Context, msg protocol.Message) ([]protocol.Recipient, *routingError) {
	if msg.Durable {
		known, err := h.store.KnownDurableAgent(ctx, msg.To)
		if err != nil {
			return nil, &routingError{protocol.ErrInternal, "cannot determine durable recipient registration"}
		}
		if known {
			return []protocol.Recipient{{AgentID: msg.To}}, nil
		}
	}
	return nil, &routingError{protocol.ErrUnknownRecipient, "no eligible recipient with id " + msg.To + "; durable offline delivery requires prior v3 registration"}
}

func (h *Hub) reportRetry(ctx context.Context, clients map[string]*Client, in Inbound) bool {
	request := in.Msg
	request.From = in.From.ID
	snapshot, err := h.store.LookupRetry(ctx, request)
	if err != nil {
		h.enqueue(clients, in.From, h.persistenceError(request.MessageID, err).frame(request.MessageID))
		return true
	}
	if snapshot == nil {
		return false
	}
	h.enqueue(clients, in.From, protocol.Message{Type: protocol.TypeAck, MessageID: request.MessageID,
		Stage: protocol.StageAccepted, Seq: snapshot.Message.Seq, Durable: true, Repeated: true,
		Recipients: publicRecipients(snapshot.Recipients), Receipts: publicReceipts(snapshot.Receipts)})
	return true
}

func disconnectDetail(durable bool) string {
	if durable {
		return "recipient session disconnected before agent acknowledgment; pending message retained for replay to its next v3 owner"
	}
	return "recipient session disconnected before agent acknowledgment; this non-durable message has no replay"
}

// publicBroadcast freezes the connected peers plus the operator inbox. The
// operator counts toward the recipient limit, and a reply must belong to a
// public thread the sender took part in.
func (h *Hub) publicBroadcast(ctx context.Context, clients map[string]*Client, in Inbound) ([]protocol.Recipient, *routingError) {
	if rerr := h.validReply(ctx, h.store.ValidatePublicReply, in); rerr != nil {
		return nil, rerr
	}
	admit, rerr := h.roomAudience(ctx, in.Msg.Room)
	if rerr != nil {
		return nil, rerr
	}
	peers, rerr := broadcastRecipients(clients, in.From, admit)
	if rerr != nil {
		return nil, rerr
	}
	if len(peers)+1 > protocol.MaxRecipients {
		return nil, &routingError{protocol.ErrRecipientUnavailable, "public broadcast exceeds 256 recipients including the operator; send to smaller groups directly"}
	}
	if rerr := checkRecipientVersions(clients, in.Msg, peers); rerr != nil {
		return nil, rerr
	}
	recipients := make([]protocol.Recipient, 0, len(peers)+1)
	recipients = append(recipients, peers...)
	recipients = append(recipients, protocol.Recipient{AgentID: protocol.OperatorID, SessionID: store.OperatorInboxSession})
	sort.Slice(recipients, func(i, j int) bool { return recipients[i].AgentID < recipients[j].AgentID })
	return recipients, nil
}

// validReply maps a correlation check to a routing error.
func (h *Hub) validReply(ctx context.Context, check func(context.Context, string, string) error, in Inbound) *routingError {
	err := check(ctx, in.From.ID, in.Msg.InReplyTo)
	if errors.Is(err, store.ErrInvalidReply) {
		return &routingError{protocol.ErrInvalidReply, err.Error()}
	}
	if err != nil {
		h.log.Error("reply validation failed", "message_id", in.Msg.MessageID, "error", err)
		return &routingError{protocol.ErrInternal, "cannot validate reply; message not accepted"}
	}
	return nil
}

// operatorInbox validates a reply's correlation before quota and persistence.
func (h *Hub) operatorInbox(ctx context.Context, in Inbound) ([]protocol.Recipient, *routingError) {
	if rerr := h.validReply(ctx, h.store.ValidateOperatorReply, in); rerr != nil {
		return nil, rerr
	}
	return []protocol.Recipient{{AgentID: protocol.OperatorID, SessionID: store.OperatorInboxSession}}, nil
}

// publicRecipients and publicReceipts hide the operator inbox's internal
// placeholder session. They copy; retained state is never modified.
func publicRecipients(in []protocol.Recipient) []protocol.Recipient {
	return mapCopy(in, func(r protocol.Recipient) protocol.Recipient {
		r.SessionID = publicSession(r.AgentID, r.SessionID)
		return r
	})
}

func publicReceipts(in []protocol.ReceiptState) []protocol.ReceiptState {
	return mapCopy(in, func(r protocol.ReceiptState) protocol.ReceiptState {
		r.SessionID = publicSession(r.AgentID, r.SessionID)
		return r
	})
}

func publicSession(agentID, session string) string {
	if agentID == protocol.OperatorID {
		return ""
	}
	return session
}

func mapCopy[T any](in []T, f func(T) T) []T {
	out := make([]T, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

// everyone admits every connected agent: the Square.
func everyone(*Client) bool { return true }
