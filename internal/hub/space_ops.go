package hub

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
)

// Default validities when an operation names none.
const (
	defaultInviteValidity  = 24 * time.Hour
	defaultMemberValidity  = pairbook.MemberValidity
	defaultAddressValidity = pairbook.AddressValidity
)

// pinned is the pin id names, or the only one, and its directory's client.
func (h *Hub) pinned(ctx context.Context, id string) (space.Pin, space.Directory, *routingError) {
	p, rerr := h.pinFor(ctx, id)
	if rerr != nil {
		return p, nil, rerr
	}
	d, rerr := h.directory(p.Base)
	return p, d, rerr
}

// remoteCall is directory work on one pinned space.
type remoteCall func(net context.Context, p space.Pin, d space.Directory) (*protocol.SpaceResult, error)

// onPin resolves the pin id names on the Run goroutine and runs call on a
// worker.
func (h *Hub) onPin(ctx context.Context, id string, call remoteCall) (spaceStep, *routingError) {
	p, d, rerr := h.pinned(ctx, id)
	if rerr != nil {
		return spaceStep{}, rerr
	}
	return spaceStep{job: func(net, _ context.Context) (*protocol.SpaceResult, error) { return call(net, p, d) }}, nil
}

func (h *Hub) spaceList(ctx context.Context, _ protocol.SpaceRequest) (spaceStep, *routingError) {
	pins, err := h.store.SpacePins(ctx)
	if err != nil {
		return spaceStep{}, spaceRefusal("space pins unavailable")
	}
	out := &protocol.SpaceResult{Pins: []protocol.SpacePin{}}
	for _, p := range pins {
		out.Pins = append(out.Pins, h.pinView(p))
	}
	return spaceStep{now: out}, nil
}

// spaceCreate makes a space this workspace owns, and pins it once the
// directory accepted it. A space ID already pinned here is refused unsent.
func (h *Hub) spaceCreate(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	if r.ID != "" {
		if _, ok, err := h.store.SpacePin(ctx, r.ID); err != nil || ok {
			return spaceStep{}, spaceRefusal("space " + r.ID + " is already pinned here; nothing was sent")
		}
	}
	return h.pinAfter(r.Directory, func(net context.Context, d space.Directory) (space.Pin, error) {
		return d.Create(net, r.ID, h.spaceName(r.Name))
	})
}

// spaceJoin files a join request under an invite link, after checking the
// link's owner and directory against any pin already held for its space:
// a conflict sends nothing, the token included.
func (h *Hub) spaceJoin(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	inv, err := space.ParseInvite(r.Link)
	if err != nil {
		return spaceStep{}, spaceRefusal(err.Error())
	}
	if rerr := h.conflicting(ctx, space.Pin{Space: inv.Space, Owner: inv.Owner, Base: inv.Base}); rerr != nil {
		return spaceStep{}, rerr
	}
	return h.pinAfter(inv.Base, func(net context.Context, d space.Directory) (space.Pin, error) {
		return d.Join(net, inv, h.spaceName(r.Name))
	})
}

// pinAfter runs remote against base's directory on a worker and pins what
// the directory accepted.
func (h *Hub) pinAfter(base string, remote func(context.Context, space.Directory) (space.Pin, error)) (spaceStep, *routingError) {
	d, rerr := h.directory(base)
	if rerr != nil {
		return spaceStep{}, rerr
	}
	return spaceStep{job: func(net, local context.Context) (*protocol.SpaceResult, error) {
		p, err := remote(net, d)
		if err != nil {
			return nil, err
		}
		return h.keep(local, p)
	}}, nil
}

func (h *Hub) spaceInvite(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	valid, rerr := validFor(r.ValidFor, defaultInviteValidity)
	if rerr != nil {
		return spaceStep{}, rerr
	}
	return h.onPin(ctx, r.Space, func(net context.Context, p space.Pin, d space.Directory) (*protocol.SpaceResult, error) {
		inv, err := d.Invite(net, p, valid)
		return &protocol.SpaceResult{Invite: inv.String()}, err
	})
}

func (h *Hub) spaceRequests(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	return h.onPin(ctx, r.Space, func(net context.Context, p space.Pin, d space.Directory) (*protocol.SpaceResult, error) {
		requests, err := d.Requests(net, p)
		return &protocol.SpaceResult{Requests: views(requests, joinRequestView)}, err
	})
}

func (h *Hub) spaceMembers(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	return h.onPin(ctx, r.Space, func(net context.Context, p space.Pin, d space.Directory) (*protocol.SpaceResult, error) {
		members, err := d.Members(net, p)
		return &protocol.SpaceResult{Members: views(members, memberView)}, err
	})
}

func views[T any](list []T, view func(T) protocol.SpaceMember) []protocol.SpaceMember {
	out := make([]protocol.SpaceMember, 0, len(list))
	for _, v := range list {
		out = append(out, view(v))
	}
	return out
}

func joinRequestView(q pairbook.JoinRequest) protocol.SpaceMember {
	return protocol.SpaceMember{Node: pairbook.KeyID(q.Node), Name: q.Name, At: time.UnixMilli(q.At).UTC()}
}

func memberView(m pairbook.Member) protocol.SpaceMember {
	return protocol.SpaceMember{Node: pairbook.KeyID(m.Node), Name: m.Name, Role: m.Role, At: time.UnixMilli(m.ExpiresAt).UTC()}
}

func (h *Hub) spacePublish(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	valid, rerr := validFor(r.ValidFor, defaultAddressValidity)
	if rerr == nil && len(r.Addresses) == 0 {
		rerr = &routingError{protocol.ErrMalformedFrame, "publish needs at least one HOST:PORT"}
	}
	if rerr != nil {
		return spaceStep{}, rerr
	}
	return h.onPin(ctx, r.Space, func(net context.Context, p space.Pin, d space.Directory) (*protocol.SpaceResult, error) {
		return nil, d.Publish(net, p, r.Addresses, valid)
	})
}

// spaceApprove countersigns the verified pending request from node, as the
// directory lists it now; a node with no such request is refused.
func (h *Hub) spaceApprove(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	valid, rerr := validFor(r.ValidFor, defaultMemberValidity)
	if rerr != nil {
		return spaceStep{}, rerr
	}
	return h.onPin(ctx, r.Space, func(net context.Context, p space.Pin, d space.Directory) (*protocol.SpaceResult, error) {
		q, err := named(d.Requests(net, p))(r.Node, func(q pairbook.JoinRequest) string { return pairbook.KeyID(q.Node) })
		if err != nil {
			return nil, err
		}
		return nil, d.Approve(net, p, q, valid)
	})
}

// spaceRevoke removes a member the pin's owner vouches for now: its key
// comes from the verified member list, since a key ID can't be reversed.
func (h *Hub) spaceRevoke(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	return h.onPin(ctx, r.Space, func(net context.Context, p space.Pin, d space.Directory) (*protocol.SpaceResult, error) {
		m, err := named(d.Members(net, p))(r.Node, func(m pairbook.Member) string { return pairbook.KeyID(m.Node) })
		if err != nil {
			return nil, err
		}
		return nil, d.Revoke(net, p, m.Node)
	})
}

// named picks, from a verified list or its error, the entry whose key ID
// is id; none is an error, so nothing unseen is ever signed for.
func named[T any](list []T, err error) func(id string, key func(T) string) (T, error) {
	return func(id string, key func(T) string) (T, error) {
		var zero T
		if err != nil {
			return zero, err
		}
		i := slices.IndexFunc(list, func(v T) bool { return key(v) == id })
		if i < 0 {
			return zero, errors.New("nothing verified from " + id)
		}
		return list[i], nil
	}
}

// spaceLeave revokes this workspace's own membership, then unpins the
// space; a failed revocation keeps the pin. The owner deletes instead.
func (h *Hub) spaceLeave(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	self := h.identity.PublicKey()
	return h.unpinAfter(ctx, r.Space, h.notOwned, func(net context.Context, p space.Pin, d space.Directory) error {
		return d.Revoke(net, p, self)
	})
}

// spaceDelete erases the space in the directory, then unpins it; a failed
// deletion keeps the pin.
func (h *Hub) spaceDelete(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	return h.unpinAfter(ctx, r.Space, h.owned, func(net context.Context, p space.Pin, d space.Directory) error {
		return d.Delete(net, p)
	})
}

func (h *Hub) owned(p space.Pin) *routingError {
	if p.Owner != h.identity.Fingerprint() {
		return spaceRefusal("only the space's owner can delete it; leave it instead")
	}
	return nil
}

func (h *Hub) notOwned(p space.Pin) *routingError {
	if p.Owner == h.identity.Fingerprint() {
		return spaceRefusal("the owner can't leave its space; delete it instead")
	}
	return nil
}

// unpinAfter checks the pin id names, unsent if check refuses it, runs
// remote on a worker, and unpins only once remote succeeded.
func (h *Hub) unpinAfter(ctx context.Context, id string, check func(space.Pin) *routingError, remote func(context.Context, space.Pin, space.Directory) error) (spaceStep, *routingError) {
	p, d, rerr := h.pinned(ctx, id)
	if rerr == nil {
		rerr = check(p)
	}
	if rerr != nil {
		return spaceStep{}, rerr
	}
	return spaceStep{job: func(net, local context.Context) (*protocol.SpaceResult, error) {
		if err := remote(net, p, d); err != nil {
			return nil, err
		}
		return nil, h.store.UnpinSpace(local, p.Space)
	}}, nil
}
