package hub

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
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
	case msg.ReviewAskRefs != nil && msg.ReviewBefore != "":
		return statusError(msg.RequestID, protocol.ErrMalformedFrame, "reviews_asked reads one request or a page, not both")
	case msg.ReviewAskRefs != nil:
		return h.reviewAskedDetail(ctx, in)
	}
	page, err := h.store.AsksBy(ctx, in.From.ID, msg.ReviewBefore)
	switch {
	case errors.Is(err, store.ErrReviewCursor):
		return statusError(msg.RequestID, protocol.ErrMalformedFrame, err.Error())
	case err != nil:
		return statusError(msg.RequestID, protocol.ErrInternal, "review requests unavailable")
	}
	return askedFrame(msg.RequestID, page)
}

// askedFrameBudget is what a page of requests may take as encoded, short
// of a frame by room for the answer's other fields.
const askedFrameBudget = protocol.MaxFrameBytes - 4096

// askedFrame is a page of requests as one frame: as many, newest first,
// as fit it encoded, at least one; the rest follow from the cursor (#303).
func askedFrame(requestID string, page store.AsksPage) protocol.Message {
	out := protocol.Message{Type: protocol.TypeReviewsAskedResult, RequestID: requestID, ReviewsAsked: []protocol.ReviewOutcomeView{}}
	size := encodedSize(out)
	for i, a := range page.Asks {
		view := a.View()
		next := encodedSize(view) + 1 // and its comma
		if i > 0 && size+next > askedFrameBudget {
			out.ReviewsAskedMore, out.ReviewsAskedNext = true, page.Cursors[i-1]
			return out
		}
		out.ReviewsAsked, size = append(out.ReviewsAsked, view), size+next
	}
	if page.More {
		out.ReviewsAskedMore, out.ReviewsAskedNext = true, page.Cursors[len(page.Cursors)-1]
	}
	return out
}

// encodedSize is v's size as frames encode it.
func encodedSize(v any) int {
	data, _ := json.Marshal(v) // protocol values always encode
	return len(data)
}

// reviewAskedDetail answers reviews_asked_get for one request, by contact
// and ask ID, with every finding received (#303): only the caller's own,
// and only exactly one.
func (h *Hub) reviewAskedDetail(ctx context.Context, in Inbound) protocol.Message {
	msg := in.Msg
	switch {
	case in.From.ProtocolVersion < protocol.ReviewFindingsVersion:
		return statusError(msg.RequestID, protocol.ErrMalformedFrame, "reviews_asked for one request requires protocol_version 21")
	case len(msg.ReviewAskRefs) != 1:
		return statusError(msg.RequestID, protocol.ErrMalformedFrame, "reviews_asked reads one request: exactly one {contact, ask_id}")
	}
	if err := protocol.ValidateReviewAskRefs(msg.ReviewAskRefs); err != nil {
		return statusError(msg.RequestID, protocol.ErrMalformedFrame, err.Error())
	}
	a, found, err := h.store.AskBy(ctx, in.From.ID, msg.ReviewAskRefs[0])
	if err != nil {
		return statusError(msg.RequestID, protocol.ErrInternal, "review requests unavailable")
	}
	views := []protocol.ReviewOutcomeView{}
	if found {
		views = append(views, a.View())
	}
	return protocol.Message{Type: protocol.TypeReviewsAskedResult, RequestID: msg.RequestID, ReviewsAsked: views}
}
