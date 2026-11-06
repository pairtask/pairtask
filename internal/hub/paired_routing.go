package hub

import (
	"context"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// homeView adds to a message this side sent who it also went to here
// (#327), as stored when it was sent; never for one received.
func (h *Hub) homeView(ctx context.Context, view *protocol.ContactMessageView, r messaging.Row) error {
	if r.Direction != messaging.DirOut {
		return nil
	}
	route, err := h.store.HomeRoutingIn(ctx, historyKey(r))
	view.Here, view.OperatorHere = route.Agents, route.Operator
	return err
}
