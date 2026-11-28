package hub

import (
	"context"
	"fmt"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// operatorAgentReads is how each agent keeps up with its durable inbox, and
// whether a session of it is connected now: what the Desktop app shows per
// agent ("3 unread since 10:02"). It reads only.
func (h *Hub) operatorAgentReads(ctx context.Context, clients map[string]*Client, req operatorRequest) protocol.Message {
	reads, err := h.store.AgentReads(ctx)
	if err != nil {
		rerr := h.inboxError("agent reads", err)
		h.auditRejected(ctx, "agent_reads", req, rerr)
		return rerr.frame("")
	}
	out := make([]protocol.AgentRead, 0, len(reads))
	for _, r := range reads {
		out = append(out, protocol.AgentRead{AgentID: r.AgentID, Online: clients[r.AgentID] != nil,
			Unread: r.Unread, OldestUnreadAt: r.OldestUnreadAt, LastReadAt: r.LastReadAt})
	}
	detail := fmt.Sprintf("agents=%d", len(out))
	if _, err := h.store.RecordOperatorAudit(ctx, store.OperatorAudit{Action: "agent_reads", PrincipalUID: req.uid, Result: store.AuditAccepted, Detail: detail}); err != nil {
		h.log.Error("operator read audit failed; agent reads not returned", "action", "agent_reads", "error", err)
		return (&routingError{protocol.ErrInternal, "operator audit unavailable"}).frame("")
	}
	return protocol.Message{Type: protocol.TypeOperatorAgentReads, AgentReads: out}
}
