package hub

import (
	"context"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// LiveClaim is a serving link claimed for a contact's connection.
type LiveClaim struct {
	h    *Hub
	link *liveLink
}

// ClaimLink claims a serving link for contact, replacing one still open.
func (h *Hub) ClaimLink(contact string) (*LiveClaim, bool) {
	l, ok := h.claimLive(contact, true)
	if !ok {
		return nil, false
	}
	return &LiveClaim{h: h, link: l}, true
}

// Run keeps the serving link until it ends, is replaced, or ctx ends;
// carried is what the first round moved.
func (c *LiveClaim) Run(ctx context.Context, open func(context.Context) messaging.Frames, carried *messaging.Counted) error {
	defer c.Release()
	return c.h.runLink(ctx, c.link, c.h.link(open, c.link, carried).RunServer)
}

// Release frees a claim that won't run.
func (c *LiveClaim) Release() { c.h.releaseLive(c.link.contact, c.link) }
