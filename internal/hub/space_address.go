package hub

import (
	"context"
	"errors"

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
	keep := store.SpaceAddress{Space: pin.Space, Addrs: r.Addresses, Valid: valid}
	return spaceStep{space: pin.Space, job: func(net, local context.Context) (*protocol.SpaceResult, error) {
		unlock := h.authority.lockSpace(spaceKey(keep.Space))
		defer unlock()
		current, err := h.addressTarget(local, keep.Space)
		if err != nil {
			return nil, err
		}
		if err := h.store.KeepSpaceAddress(local, keep); err != nil {
			return nil, err
		}
		return nil, h.publishAddress(net, local, current, keep)
	}}, nil
}

// addressTarget is space's pin as kept now, under the space's lock, when
// its active map binds this workspace; a space unpinned or left since the
// request was made is refused, so nothing is kept or published for it.
func (h *Hub) addressTarget(ctx context.Context, id string) (space.Pin, error) {
	pin, found, err := h.store.SpacePin(ctx, id)
	switch {
	case err != nil:
		return pin, err
	case !found:
		return pin, errors.New("space " + id + " is not pinned here")
	case !h.boundHere(id):
		return pin, errors.New("this workspace isn't in space " + id + " yet; publish once the owner has admitted it")
	}
	return pin, nil
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
	a, due := h.dueAddress(ctx, id)
	if !due {
		return
	}
	pin, err := h.addressTarget(ctx, id)
	if err != nil {
		return
	}
	if err := h.publishAddress(ctx, ctx, pin, a); err != nil {
		h.log.Info("space address not renewed", "space", id, "err", err)
	}
}

// dueAddress is the address setting kept for space now, if it is due.
func (h *Hub) dueAddress(ctx context.Context, id string) (store.SpaceAddress, bool) {
	a, found, err := h.store.SpaceAddressOf(ctx, id)
	if err != nil || !found {
		return a, false
	}
	return a, h.addressDue(a)
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
