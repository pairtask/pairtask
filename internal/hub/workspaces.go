package hub

import (
	"context"

	"github.com/pairtask/pairtask/internal/protocol"
)

// workspaces answers an agent's workspaces_get (#222): the paired
// workspaces it is shared with, the only ones it may write to, with what
// each says it shares, a hint for addressing.
func (h *Hub) workspaces(ctx context.Context, in Inbound) protocol.Message {
	msg := in.Msg
	switch {
	case msg.RequestID == "" || len(msg.RequestID) > 128:
		return statusError(msg.RequestID, protocol.ErrMalformedFrame, "request_id is required, at most 128 bytes")
	case in.From.ProtocolVersion < protocol.WorkspaceVersion:
		return statusError(msg.RequestID, protocol.ErrMalformedFrame, "workspaces require protocol_version 13")
	}
	contacts, err := h.store.AgentWorkspaces(ctx, in.From.ID)
	if err != nil {
		return statusError(msg.RequestID, protocol.ErrInternal, "paired workspaces unavailable")
	}
	views := []protocol.WorkspaceView{}
	for _, c := range contacts {
		agents, err := h.store.PeerMessages().PeerDirectory(ctx, c.IdentityKey)
		if err != nil {
			return statusError(msg.RequestID, protocol.ErrInternal, "paired workspaces unavailable")
		}
		views = append(views, protocol.WorkspaceView{Name: c.Name, Key: c.IdentityKey, Live: h.Live(c.IdentityKey), Agents: agents})
	}
	return protocol.Message{Type: protocol.TypeWorkspacesResult, RequestID: msg.RequestID, Workspaces: views}
}
