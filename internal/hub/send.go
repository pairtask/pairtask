package hub

import (
	"context"
	"encoding/json"
	"github.com/pairtask/pairtask/internal/store"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// sendAsAgent routes a message an agent sends: prepared, admitted, persisted
// with its recipients and delivered, the sender told of acceptance. A
// retry of an id already routed gets its retained outcome and is not sent
// again. The broker posts an agent's decision anchor through this too.
func (h *Hub) sendAsAgent(ctx context.Context, clients map[string]*Client, in Inbound) *routingError {
	if err := h.prepareAgentSend(ctx, &in); err != nil {
		return err
	}
	if !h.newSend(ctx, clients, in) {
		return nil
	}
	recipients, err := h.resolveRecipients(ctx, clients, in)
	if err != nil {
		return err
	}
	out, audience, err := h.persistRoute(ctx, in, recipients)
	if err != nil {
		return err
	}
	recipients = audience.Recipients
	h.noteRouted(ctx, out, recipients)
	if out.AckRequested {
		h.enqueue(clients, in.From, protocol.Message{Type: protocol.TypeAck, MessageID: out.MessageID, Stage: protocol.StageAccepted, Seq: out.Seq, Recipients: h.advised(ctx, clients, out, publicRecipients(recipients)), Excluded: audience.Excluded, Durable: out.Durable})
	}
	h.deliver(clients, in.From, out, recipients)
	return nil
}

func (h *Hub) persistRoute(ctx context.Context, in Inbound, recipients []protocol.Recipient) (protocol.Message, store.BroadcastAudience, *routingError) {
	audience := store.BroadcastAudience{Recipients: recipients}
	h.seq++
	ts := time.Now().UTC()
	msg := in.Msg
	out := protocol.Message{
		Type: protocol.TypeMsg, Seq: h.seq, TS: &ts,
		From: in.From.ID, SessionID: in.From.SessionID, To: msg.To,
		Payload: msg.Payload, MessageID: msg.MessageID, Origin: protocol.OriginAgent,
		InReplyTo: msg.InReplyTo, AckRequested: msg.AckRequested, Durable: msg.Durable, Room: msg.Room,
	}
	// Added broker metadata must still fit the receiver's frame limit.
	sized := out
	sized.Replayed = out.Durable
	encoded, err := json.Marshal(sized)
	if err != nil || len(encoded)+1 > protocol.MaxFrameBytes {
		return out, audience, &routingError{protocol.ErrMalformedFrame, "routed message exceeds the 1 MiB frame limit"}
	}
	if isPublic(msg.To, in.From.ProtocolVersion) {
		audience, err = h.store.PersistPublicBroadcast(ctx, out, recipients)
	} else {
		err = h.store.PersistMessage(ctx, out, in.From.SessionID, recipients)
	}
	if err != nil {
		return out, audience, h.refusedSend(ctx, in, recipients, err)
	}
	return out, audience, nil
}
