package hub

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
)

// Status in Pairbook v1 spaces: a heartbeat every presenceEvery while this
// workspace is in a space, the agents the operator lists there, and who
// this workspace may see. Listing advertises an agent; who may message it
// is still the contact sharing.

func init() {
	spaceOps[protocol.SpaceOpMapShare] = (*Hub).mapShare
	spaceOps[protocol.SpaceOpMapUnshare] = (*Hub).mapUnshare
	spaceOps[protocol.SpaceOpMapPeople] = (*Hub).mapPeople
}

const (
	presenceEvery  = time.Minute
	noStatusPause  = time.Hour // a Pairbook without status is asked again after this
	maxSpaceAgents = 32
)

// statusless remembers Pairbooks that keep no status, so they aren't asked
// every minute.
type statusless struct {
	mu    sync.Mutex
	until map[string]time.Time
}

func (s *statusless) skip(base string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return now.Before(s.until[base])
}

func (s *statusless) note(base string, err error, now time.Time) {
	if !errors.Is(err, space.ErrNoStatus) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.until == nil {
		s.until = map[string]time.Time{}
	}
	s.until[base] = now.Add(noStatusPause)
}

// statuser is pin's Pairbook's status calls, unless it is known to keep
// none.
func (h *Hub) statuser(pin space.Pin) (space.Statuser, bool) {
	if h.noStatus.skip(pin.Base, h.authority.now()) {
		return nil, false
	}
	dir, err := h.mapDirectory(pin.Base)
	if err != nil {
		return nil, false
	}
	st, ok := dir.(space.Statuser)
	return st, ok
}

// presencePass sends a heartbeat for every space whose map binds this
// workspace.
func (h *Hub) presencePass(ctx context.Context) {
	pins, err := h.store.SpacePins(ctx)
	if err != nil {
		return
	}
	for _, pin := range pins {
		if !h.boundHere(pin.Space) {
			continue
		}
		if st, ok := h.statuser(pin); ok {
			h.noStatus.note(pin.Base, st.Heartbeat(ctx, pin), h.authority.now())
			h.fetchStatus(ctx, pin, st)
		}
	}
}

// publishAllAgents sends each bound space its listed agents.
func (h *Hub) publishAllAgents(ctx context.Context) {
	pins, err := h.store.SpacePins(ctx)
	if err != nil {
		return
	}
	for _, pin := range pins {
		if h.boundHere(pin.Space) {
			_ = h.publishAgents(ctx, pin)
		}
	}
}

// publishAgents sends pin's space the agents listed there, with their roles.
func (h *Hub) publishAgents(ctx context.Context, pin space.Pin) error {
	st, ok := h.statuser(pin)
	if !ok {
		return space.ErrNoStatus
	}
	agents, err := h.listedAgents(ctx, pin.Space)
	if err != nil {
		return err
	}
	err = st.PublishAgents(ctx, pin, agents)
	h.noStatus.note(pin.Base, err, h.authority.now())
	return err
}

func (h *Hub) listedAgents(ctx context.Context, id string) ([]space.Agent, error) {
	names, err := h.store.SpaceShares(ctx, id)
	if err != nil {
		return nil, err
	}
	roles, err := h.store.AgentRoles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]space.Agent, 0, len(names))
	for _, n := range names {
		out = append(out, space.Agent{Name: n, Role: roles[n]})
	}
	return out, nil
}

// mapShare lists an agent in a space and sends the list at once.
func (h *Hub) mapShare(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	return h.shareStep(ctx, r, true)
}

// mapUnshare stops listing an agent in a space.
func (h *Hub) mapUnshare(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	return h.shareStep(ctx, r, false)
}

func (h *Hub) shareStep(ctx context.Context, r protocol.SpaceRequest, share bool) (spaceStep, *routingError) {
	if !pairbook.ValidName(r.Agent) {
		return spaceStep{}, spaceRefusal("name the agent to list")
	}
	pin, rerr := h.pinFor(ctx, r.Space)
	if rerr != nil {
		return spaceStep{}, rerr
	}
	return spaceStep{space: pin.Space, job: func(net, local context.Context) (*protocol.SpaceResult, error) {
		if err := h.changeShare(local, pin.Space, r.Agent, share); err != nil {
			return nil, err
		}
		shares, err := h.store.SpaceShares(local, pin.Space)
		if err != nil {
			return nil, err
		}
		if h.boundHere(pin.Space) {
			_ = h.publishAgents(net, pin) // a failure is retried by the next refresh
		}
		return &protocol.SpaceResult{Shares: shares}, nil
	}}, nil
}

func (h *Hub) changeShare(ctx context.Context, id, agent string, share bool) error {
	if !share {
		return h.store.UnshareInSpace(ctx, id, agent)
	}
	shares, err := h.store.SpaceShares(ctx, id)
	if err != nil {
		return err
	}
	if len(shares) >= maxSpaceAgents && !slices.Contains(shares, agent) {
		return errors.New("at most 32 agents can be listed in a space")
	}
	return h.store.ShareInSpace(ctx, id, agent, h.authority.now())
}

var errNotInSpace = errors.New("this workspace isn't in the space now: no current map binds it")

// mapPeople is who this workspace may see in a space: itself and the
// workspaces its current map grants it, by person, with their status. A
// Pairbook that keeps no status gives the signed map's view, status
// unknown; any other refusal (a 403 above all) is the answer.
func (h *Hub) mapPeople(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	pin, rerr := h.pinFor(ctx, r.Space)
	if rerr != nil {
		return spaceStep{}, rerr
	}
	return spaceStep{space: pin.Space, job: func(net, local context.Context) (*protocol.SpaceResult, error) {
		shown, err := h.visible(pin.Space)
		if err != nil {
			return nil, err
		}
		people, known, err := h.remotePeople(net, pin)
		if err != nil {
			return nil, err
		}
		if !known {
			return h.mapOnlyPeople(local, pin.Space, shown)
		}
		return &protocol.SpaceResult{People: peopleView(people, shown), StatusKnown: true}, nil
	}}, nil
}

// visible is this workspace and the workspaces its current map grants it.
func (h *Hub) visible(id string) (map[string]bool, error) {
	v, ok := h.SpaceAuthority(id)
	ep, err := h.PairingEndpoint()
	if !ok || err != nil {
		return nil, errNotInSpace
	}
	me := ep.Identity.Fingerprint()
	if _, bound := v.BoundTo(me); !bound {
		return nil, errNotInSpace
	}
	peers, err := v.Peers(me, h.authority.now())
	if err != nil {
		return nil, errNotInSpace
	}
	shown := map[string]bool{me: true}
	for _, p := range peers {
		shown[p.Workspace] = true
	}
	return shown, nil
}

// remotePeople is the Pairbook's status; known is false only when it keeps
// none.
func (h *Hub) remotePeople(ctx context.Context, pin space.Pin) ([]space.PersonStatus, bool, error) {
	st, ok := h.statuser(pin)
	if !ok {
		return nil, false, nil
	}
	people, err := st.Status(ctx, pin)
	h.noStatus.note(pin.Base, err, h.authority.now())
	if errors.Is(err, space.ErrNoStatus) {
		return nil, false, nil
	}
	return people, err == nil, err
}

// mapOnlyPeople is the stored map's people with the workspaces shown
// admits, status unknown.
func (h *Hub) mapOnlyPeople(ctx context.Context, id string, shown map[string]bool) (*protocol.SpaceResult, error) {
	m, _, found, err := h.store.SpaceMap(ctx, id)
	if err != nil || !found {
		return nil, errors.Join(err, errNotInSpace)
	}
	bound := map[pairbook.Lineage][]space.WorkspaceStatus{}
	for _, b := range m.Bindings {
		if l, ok := b.ClaimedLineage(); ok {
			bound[l] = append(bound[l], space.WorkspaceStatus{Workspace: b.Workspace()})
		}
	}
	people := make([]space.PersonStatus, 0, len(m.People))
	for _, p := range m.People {
		people = append(people, space.PersonStatus{Person: pairbook.LineageHex(p.Lineage), Name: p.Name, Role: p.Role, Workspaces: bound[p.Lineage]})
	}
	return &protocol.SpaceResult{People: peopleView(people, shown)}, nil
}

// peopleView is people with only the workspaces shown admits; a person
// left with none is left out.
func peopleView(people []space.PersonStatus, shown map[string]bool) []protocol.SpacePerson {
	out := make([]protocol.SpacePerson, 0, len(people))
	for _, p := range people {
		v := protocol.SpacePerson{Person: p.Person, Name: p.Name, Role: p.Role}
		for _, w := range p.Workspaces {
			if shown[w.Workspace] {
				v.Workspaces = append(v.Workspaces, workspaceView(w))
			}
		}
		if len(v.Workspaces) > 0 {
			out = append(out, v)
		}
	}
	return out
}

func workspaceView(w space.WorkspaceStatus) protocol.SpaceWorkspace {
	v := protocol.SpaceWorkspace{Workspace: w.Workspace, Online: w.Online, LastSeen: w.LastSeen}
	for _, a := range w.Agents {
		v.Agents = append(v.Agents, protocol.SpaceAgent{Name: a.Name, Role: a.Role, Summary: a.Summary})
	}
	return v
}
