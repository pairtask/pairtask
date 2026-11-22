package hub

import (
	"context"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/store"
	"github.com/pairtask/pairtask/pairbook"
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
	if !pairbook.ValidAddresses(r.Addresses) {
		return spaceStep{}, &routingError{protocol.ErrMalformedFrame, "addresses must be 1 to 8 HOST:PORT"}
	}
	pin, rerr := h.pinFor(ctx, r.Space)
	if rerr != nil {
		return spaceStep{}, rerr
	}
	if !h.boundHere(pin.Space) {
		return spaceStep{}, spaceRefusal("this workspace isn't in space " + pin.Space + " yet; publish once the owner has admitted it")
	}
	keep := store.SpaceAddress{Space: pin.Space, Addrs: r.Addresses, Valid: valid}
	return spaceStep{space: pin.Space, job: func(net, local context.Context) (*protocol.SpaceResult, error) {
		unlock := h.authority.lockSpace(spaceKey(pin.Space))
		defer unlock()
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
		h.renewAddress(ctx, a.Space)
	}
}

// renewAddress renews space's address under its lock, from the setting
// kept now (not a copy read before the lock), if due and still bound.
func (h *Hub) renewAddress(ctx context.Context, id string) {
	unlock := h.authority.lockSpace(spaceKey(id))
	defer unlock()
	a, found, err := h.store.SpaceAddressOf(ctx, id)
	if err != nil || !found || !h.addressDue(a) || !h.boundHere(id) {
		return
	}
	pin, found, err := h.store.SpacePin(ctx, id)
	if err != nil || !found {
		return
	}
	if err := h.publishAddress(ctx, ctx, pin, a); err != nil {
		h.log.Info("space address not renewed", "space", id, "err", err)
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
