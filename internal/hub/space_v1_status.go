package hub

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/pairbook"
)

// Space map status, and the refresher (Pairbook v1). Every pinned space is
// refreshed every refreshEvery and soon after a request is sent, so a new
// member's workspace takes effect once the owner's map includes it, with no
// owner action on this side. Each pass first resends what the outbox still
// holds, the same bytes as before.

const refreshEvery = 5 * time.Minute

// mapStatus is how each pinned space stands.
func (h *Hub) mapStatus(ctx context.Context, _ protocol.SpaceRequest) (spaceStep, *routingError) {
	pins, err := h.store.SpacePins(ctx)
	if err != nil {
		return spaceStep{}, spaceRefusal(err.Error())
	}
	local, err := h.localContacts(ctx)
	if err != nil {
		return spaceStep{}, spaceRefusal(err.Error())
	}
	out := &protocol.SpaceResult{Maps: []protocol.SpaceMapStatus{}}
	for _, pin := range pins {
		st, err := h.spaceMapStatus(ctx, pin.Space, local)
		if err != nil {
			return spaceStep{}, spaceRefusal(err.Error())
		}
		out.Maps = append(out.Maps, st)
	}
	return spaceStep{now: out}, nil
}

// localContacts is what status reads once: this workspace's key ID, the
// space-managed contacts and the operator's refusals.
type localContacts struct {
	self    string
	managed map[string]int64
	refused []string
}

func (h *Hub) localContacts(ctx context.Context) (localContacts, error) {
	var l localContacts
	if ep, err := h.PairingEndpoint(); err == nil {
		l.self = ep.Identity.Fingerprint()
	}
	var err error
	if l.managed, err = h.store.ManagedSpaceContacts(ctx); err != nil {
		return l, err
	}
	l.refused, err = h.store.SpaceRefusals(ctx)
	return l, err
}

// spaceMapStatus is one space's state: pending until a map binds this
// workspace, refused when the directory turned it away after that,
// expired, or active; with the managed contacts and refused workspaces
// this space's map names as peers.
func (h *Hub) spaceMapStatus(ctx context.Context, id string, l localContacts) (protocol.SpaceMapStatus, error) {
	st := protocol.SpaceMapStatus{Space: id, State: "pending", Refresh: h.SpaceRefreshError(id)}
	m, mark, found, err := h.store.SpaceMap(ctx, id)
	if err != nil || !found {
		return st, err
	}
	st.Mark, st.ExpiresAt = markView(mark), time.UnixMilli(m.ExpiresAt).UTC()
	v, active := h.SpaceAuthority(id)
	if _, bound := v.BoundTo(l.self); active && !bound {
		return st, nil // a map, but not with this workspace in it yet
	}
	st.State = stateOf(active, h.refusedByDirectory(id))
	st.Managed, st.Refused = h.peersAmong(v, l)
	return st, nil
}

func stateOf(active, refused bool) string {
	switch {
	case refused:
		return "refused"
	case active:
		return "active"
	}
	return "expired"
}

// peersAmong are the managed contacts and refused workspaces among v's
// peers of this workspace.
func (h *Hub) peersAmong(v pairbook.VerifiedMap, l localContacts) (managed, refused []string) {
	peers, err := v.Peers(l.self, h.authority.now())
	if err != nil {
		return nil, nil
	}
	for _, p := range peers {
		if _, ok := l.managed[p.Workspace]; ok {
			managed = append(managed, p.Workspace)
		}
		if slices.Contains(l.refused, p.Workspace) {
			refused = append(refused, p.Workspace)
		}
	}
	return managed, refused
}

// refusedByDirectory: the last refresh was a 403.
func (h *Hub) refusedByDirectory(id string) bool {
	return h.authority.refusedByDirectory(spaceKey(id))
}

// spaceRefresher runs refresh passes until stopped; at most one at a time.
type spaceRefresher struct {
	kick   chan struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// startSpaceRefresher runs passes now, on a kick, and every refreshEvery.
func (h *Hub) startSpaceRefresher(ctx context.Context) {
	if h.spaces == nil || h.store == nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	r := &spaceRefresher{kick: make(chan struct{}, 1), cancel: cancel}
	h.refresher = r
	r.wg.Add(1)
	go r.loop(ctx, h)
	h.kickRefresh()
}

// stopSpaceRefresher cancels the passes and waits for them.
func (h *Hub) stopSpaceRefresher() {
	if r := h.refresher; r != nil {
		r.cancel()
		r.wg.Wait()
	}
}

// kickRefresh asks for a pass soon.
func (h *Hub) kickRefresh() {
	if r := h.refresher; r != nil {
		select {
		case r.kick <- struct{}{}:
		default:
		}
	}
}

func (r *spaceRefresher) loop(ctx context.Context, h *Hub) {
	defer r.wg.Done()
	tick := time.NewTicker(refreshEvery)
	defer tick.Stop()
	beat := time.NewTicker(presenceEvery)
	defer beat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-beat.C:
			h.presencePass(ctx)
			continue
		case <-r.kick:
		case <-tick.C:
		}
		h.refreshPass(ctx)
		h.presencePass(ctx)
	}
}

// refreshPass resends the outbox, then refreshes every pinned space.
func (h *Hub) refreshPass(ctx context.Context) {
	h.resendOutbox(ctx)
	defer h.publishAllAgents(ctx) // after the maps, so a new binding counts
	defer h.renewAddresses(ctx)   // after the maps, so a new grant counts
	pins, err := h.store.SpacePins(ctx)
	if err != nil {
		return
	}
	for _, pin := range pins {
		_ = h.RefreshSpace(ctx, pin.Space) // its error is kept for status
	}
}
