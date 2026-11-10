package hub

import (
	"context"

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
	out, err := h.persistRoute(ctx, in, recipients)
	if err != nil {
		return err
	}
	h.noteRouted(ctx, out, recipients)
	if out.AckRequested {
		h.enqueue(clients, in.From, protocol.Message{Type: protocol.TypeAck, MessageID: out.MessageID, Stage: protocol.StageAccepted, Seq: out.Seq, Recipients: publicRecipients(recipients), Durable: out.Durable})
	}
	h.deliver(clients, in.From, out, recipients)
	return nil
}
