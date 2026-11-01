package hub

import (
	"context"

	"github.com/pairtask/pairtask/internal/protocol"
)

// workspaceFrames are an agent's requests about paired workspaces.
var workspaceFrames = map[string]func(*Hub, context.Context, Inbound) protocol.Message{
	protocol.TypeWorkspacesGet:   (*Hub).workspaces,
	protocol.TypeReviewsAskedGet: (*Hub).reviewsAsked,
}

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
		views = append(views, protocol.WorkspaceView{Name: c.Name, LocalName: c.LocalName, Key: c.IdentityKey, Live: h.Live(c.IdentityKey), Agents: agents})
	}
	return protocol.Message{Type: protocol.TypeWorkspacesResult, RequestID: msg.RequestID, Workspaces: views}
}

// reviewsAsked answers an agent's reviews_asked_get: the review requests it
// sent paired workspaces, never anyone else's.
func (h *Hub) reviewsAsked(ctx context.Context, in Inbound) protocol.Message {
	msg := in.Msg
	switch {
	case msg.RequestID == "" || len(msg.RequestID) > 128:
		return statusError(msg.RequestID, protocol.ErrMalformedFrame, "request_id is required, at most 128 bytes")
	case in.From.ProtocolVersion < protocol.AskedVersion:
		return statusError(msg.RequestID, protocol.ErrMalformedFrame, "reviews_asked requires protocol_version 15")
	}
	asks, err := h.store.AsksBy(ctx, in.From.ID)
	if err != nil {
		return statusError(msg.RequestID, protocol.ErrInternal, "review requests unavailable")
	}
	views := []protocol.ReviewOutcomeView{}
	for _, a := range asks {
		views = append(views, a.View())
	}
	return protocol.Message{Type: protocol.TypeReviewsAskedResult, RequestID: msg.RequestID, ReviewsAsked: views}
}
