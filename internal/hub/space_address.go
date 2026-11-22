package hub

import (
	"context"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/store"
)

// Publishing this workspace's address in a Pairbook v1 space: the
// operator names the addresses once; the broker signs a fresh record each
// time and renews it at half its validity, only while the space's active
// map binds this workspace. The directory keeps only the newest record.

func init() {
	spaceOps[protocol.SpaceOpMapAddress] = (*Hub).mapAddress
}

// mapAddress keeps the addresses for the space and publishes them now.
func (h *Hub) mapAddress(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	valid, rerr := addressValidity.of(r.ValidFor)
	if rerr == nil && len(r.Addresses) == 0 {
		rerr = &routingError{protocol.ErrMalformedFrame, "publish needs at least one HOST:PORT"}
	}
	if rerr != nil {
		return spaceStep{}, rerr
	}
	pin, rerr := h.pinFor(ctx, r.Space)
	if rerr != nil {
		return spaceStep{}, rerr
	}
	keep := store.SpaceAddress{Space: pin.Space, Addrs: r.Addresses, Valid: valid}
	return spaceStep{space: pin.Space, job: func(net, local context.Context) (*protocol.SpaceResult, error) {
		if err := h.store.KeepSpaceAddress(local, keep); err != nil {
			return nil, err
		}
		return nil, h.publishAddress(net, local, pin, keep)
	}}, nil
}

// publishAddress signs and sends a's record and notes when.
func (h *Hub) publishAddress(net, local context.Context, pin space.Pin, a store.SpaceAddress) error {
	dir, err := h.mapDirectory(pin.Base)
	if err != nil {
		return err
	}
	if err := dir.PublishAddress(net, pin, a.Addrs, a.Valid); err != nil {
		return err
	}
	return h.store.PublishedSpaceAddress(local, pin.Space, h.authority.now())
}

// renewAddresses republishes each kept address at half its validity, in
// spaces whose active map binds this workspace.
func (h *Hub) renewAddresses(ctx context.Context) {
	kept, err := h.store.SpaceAddresses(ctx)
	if err != nil {
		return
	}
	for _, a := range kept {
		if h.addressDue(a) && h.boundHere(a.Space) {
			h.renewAddress(ctx, a)
		}
	}
}

func (h *Hub) renewAddress(ctx context.Context, a store.SpaceAddress) {
	pin, found, err := h.store.SpacePin(ctx, a.Space)
	if err != nil || !found {
		return
	}
	if err := h.publishAddress(ctx, ctx, pin, a); err != nil {
		h.log.Info("space address not renewed", "space", a.Space, "err", err)
	}
}

// addressDue: never published, or half its validity has passed.
func (h *Hub) addressDue(a store.SpaceAddress) bool {
	return a.PublishedAt.IsZero() || !h.authority.now().Before(a.PublishedAt.Add(a.Valid/2))
}

// boundHere: the space's active map binds this workspace.
func (h *Hub) boundHere(id string) bool {
	ep, err := h.PairingEndpoint()
	if err != nil {
		return false
	}
	v, ok := h.SpaceAuthority(id)
	if !ok {
		return false
	}
	_, bound := v.BoundTo(ep.Identity.Fingerprint())
	return bound
}
