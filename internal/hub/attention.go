package hub

import (
	"context"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// Attention (#422): the operator turns one agent's do-not-disturb on or
// off; while it is on, messages still reach the agent's adapter, stamped
// so it keeps them without interrupting, except the operator's own urgent
// ones. The broker decides for each recipient when the message is
// admitted and stamps every delivered frame itself: nothing a sender
// writes into attention or urgent survives, and a paired workspace is
// never urgent and never told about DND.

// attentionFrames are the agent frames attention has.
var attentionFrames = map[string]bool{protocol.TypeAttentionSubmitted: true}

// enforces: the client's adapter keeps a suppressed message quiet.
func enforces(c *Client) bool { return c != nil && c.ProtocolVersion >= protocol.AttentionVersion }

// operatorAttention sets or reads one agent's DND.
func (h *Hub) operatorAttention(ctx context.Context, clients map[string]*Client, req operatorRequest) protocol.Message {
	msg := req.msg
	if rerr := checkAttentionRequest(msg); rerr != nil {
		return rerr.frame(msg.MessageID)
	}
	agent := msg.AgentID
	if msg.AttentionOp != protocol.AttentionStatus {
		changed, err := h.store.SetAttention(ctx, agent, msg.AttentionOp == protocol.AttentionOn, h.contactNow())
		if err != nil {
			h.log.Error("attention not set", "agent_id", agent, "error", err)
			return (&routingError{protocol.ErrInternal, "attention setting not stored"}).frame(msg.MessageID)
		}
		if changed {
			h.sendSummary(ctx, clients, clients[agent]) // nothing while DND is on or none is owed
		}
	}
	state, rerr := h.attentionState(ctx, clients, agent)
	if rerr != nil {
		return rerr.frame(msg.MessageID)
	}
	return protocol.Message{Type: protocol.TypeOperatorAttentionResult, MessageID: msg.MessageID, AttentionState: state}
}

// checkAttentionRequest: one valid agent and a known operation.
func checkAttentionRequest(msg protocol.Message) *routingError {
	if err := protocol.ValidateAgentID(msg.AgentID); err != nil {
		return &routingError{protocol.ErrMalformedFrame, "agent_id names one local agent: " + err.Error()}
	}
	switch msg.AttentionOp {
	case protocol.AttentionOn, protocol.AttentionOff, protocol.AttentionStatus:
		return nil
	}
	return &routingError{protocol.ErrMalformedFrame, "attention_op must be on, off or status"}
}

// attentionState is the agent's DND as the operator reads it: stored, and
// whether the adapter connected now enforces it.
func (h *Hub) attentionState(ctx context.Context, clients map[string]*Client, agent string) (*protocol.AttentionState, *routingError) {
	a, err := h.store.ReadAttention(ctx, agent)
	if err != nil {
		h.log.Error("attention not read", "agent_id", agent, "error", err)
		return nil, &routingError{protocol.ErrInternal, "attention setting unavailable"}
	}
	state := &protocol.AttentionState{AgentID: agent, DND: a.DND, Since: a.Since, Pending: a.Pending, InboxUsed: a.InboxUsed,
		InboxLimit: store.MaxPendingMessages}
	if a.DND {
		state.Reason = unenforced(clients[agent])
		state.Enforced = state.Reason == ""
	}
	return state, nil
}

// unenforced is why a DND that is on isn't enforced for the client now,
// "" when it is.
func unenforced(c *Client) string {
	switch {
	case c == nil:
		return protocol.AttentionReasonOffline
	case !enforces(c):
		return protocol.AttentionReasonOldAdapter
	}
	return ""
}

// noteEnforcement records, as a client connects or leaves, whether its
// agent's DND can be decided at admit: not while an adapter that predates
// it is connected.
func (h *Hub) noteEnforcement(c *Client, connected bool) {
	if enforces(c) && !connected {
		return // nothing changed while it was here
	}
	if err := h.store.SetAttentionEnforced(context.Background(), c.ID, !connected || enforces(c)); err != nil {
		h.log.Error("attention enforcement not recorded", "agent_id", c.ID, "error", err)
	}
}

// stamped is a msg frame as client c gets it: with the broker's
// disposition for that recipient and nothing a sender claimed. A durable
// message carries what was decided at its admit; one that isn't durable
// is quiet while the recipient's DND is on.
func (h *Hub) stamped(c *Client, msg protocol.Message) protocol.Message {
	if msg.Type != protocol.TypeMsg {
		return msg
	}
	msg.Attention, msg.Urgent = "", false
	if !enforces(c) {
		return msg
	}
	disposition, err := h.dispositionFor(c, msg)
	if err != nil {
		h.log.Error("attention not read; message notifies", "agent_id", c.ID, "message_id", msg.MessageID, "error", err)
		return msg
	}
	msg.Attention = disposition
	return msg
}

func (h *Hub) dispositionFor(c *Client, msg protocol.Message) (string, error) {
	ctx := context.Background()
	if msg.Durable {
		return h.store.Disposition(ctx, msg.MessageID, c.ID)
	}
	a, err := h.store.ReadAttention(ctx, c.ID)
	if err != nil || !a.DND {
		return "", err
	}
	return protocol.AttentionSuppressed, nil
}

// sendSummary sends the agent the summary it is owed, if any: once DND is
// off, to an adapter that can show it. The adapter confirms it.
func (h *Hub) sendSummary(ctx context.Context, clients map[string]*Client, c *Client) {
	if !enforces(c) {
		return
	}
	summary, err := h.store.OwedSummary(ctx, c.ID)
	if err != nil {
		h.log.Error("attention summary not read", "agent_id", c.ID, "error", err)
		return
	}
	if summary != nil {
		h.enqueue(clients, c, protocol.Message{Type: protocol.TypeAttentionSummary, AttentionSummary: summary})
	}
}

// handleAttention takes an adapter's receipt for a summary: accepted only
// for the generation owed to that very agent now.
func (h *Hub) handleAttention(ctx context.Context, clients map[string]*Client, in Inbound) {
	accepted, err := h.store.SummarySubmitted(ctx, in.From.ID, in.Msg.AttentionGeneration)
	if err != nil {
		h.sendError(clients, in.From, protocol.ErrInternal, "summary receipt not stored", in.Msg.MessageID)
		return
	}
	h.enqueue(clients, in.From, protocol.Message{Type: protocol.TypeAttentionSubmittedResult, AttentionGeneration: in.Msg.AttentionGeneration, Accepted: &accepted})
}

// advised are an accepted message's recipients as its local sender is
// told of them: with what each one's DND does to it, when it is on.
func (h *Hub) advised(ctx context.Context, clients map[string]*Client, msg protocol.Message, recipients []protocol.Recipient) []protocol.Recipient {
	return mapCopy(recipients, func(r protocol.Recipient) protocol.Recipient {
		r.Attention = h.advisory(ctx, clients[r.AgentID], advisedMessage{msg.MessageID, r.AgentID, msg.Durable})
		return r
	})
}

// advisedMessage is one message for one recipient, and whether it was
// admitted durably: one that wasn't has no stored decision and is quiet
// whenever the recipient's DND is.
type advisedMessage struct {
	id, agent string
	durable   bool
}

// advisory is what a sender is told of one recipient: nothing while its
// DND is off or the message goes through it as urgent.
func (h *Hub) advisory(ctx context.Context, c *Client, m advisedMessage) string {
	a, err := h.store.ReadAttention(ctx, m.agent)
	if err != nil || !a.DND {
		return ""
	}
	switch {
	case c == nil:
		return protocol.AttentionOffline
	case !enforces(c):
		return protocol.AttentionUnsupported
	}
	if !m.durable {
		return protocol.AttentionSuppressed
	}
	disposition, err := h.store.Disposition(ctx, m.id, m.agent)
	if err != nil || disposition != protocol.AttentionSuppressed {
		return ""
	}
	return protocol.AttentionSuppressed
}
