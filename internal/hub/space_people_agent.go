package hub

import (
	"context"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
)

// What an agent may read of its workspace's spaces: the status the broker
// last fetched with its heartbeat, filtered as the operator's view is, to
// this workspace and what its current map grants it. Answered locally, so
// an agent never makes the broker call the Pairbook.

// statusCache is each space's last status from its Pairbook.
type statusCache struct {
	mu     sync.Mutex
	spaces map[string]cachedStatus
}

type cachedStatus struct {
	people []space.PersonStatus
	at     time.Time
}

func (c *statusCache) put(id string, people []space.PersonStatus, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.spaces == nil {
		c.spaces = map[string]cachedStatus{}
	}
	c.spaces[id] = cachedStatus{people: people, at: at}
}

// get is id's status if fetched within maxAge of now.
func (c *statusCache) get(id string, now time.Time) ([]space.PersonStatus, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.spaces[id]
	if !ok || now.Sub(s.at) > statusMaxAge {
		return nil, false
	}
	return s.people, true
}

const statusMaxAge = 3 * presenceEvery

// fetchStatus keeps pin's status for agents, after a heartbeat.
func (h *Hub) fetchStatus(ctx context.Context, pin space.Pin, st space.Statuser) {
	people, err := st.Status(ctx, pin)
	h.noStatus.note(pin.Base, err, h.authority.now())
	if err == nil {
		h.statusCache.put(pin.Space, people, h.authority.now())
	}
}

// spacePeople answers an agent's space_people_get.
func (h *Hub) spacePeople(ctx context.Context, in Inbound) protocol.Message {
	msg := in.Msg
	switch {
	case msg.RequestID == "" || len(msg.RequestID) > 128:
		return statusError(msg.RequestID, protocol.ErrMalformedFrame, "request_id is required, at most 128 bytes")
	case in.From.ProtocolVersion < protocol.SpacePeopleVersion:
		return statusError(msg.RequestID, protocol.ErrMalformedFrame, "space_members requires protocol_version 33")
	}
	result, rerr := h.agentSpacePeople(ctx, msg.SpaceRequest)
	if rerr != nil {
		return statusError(msg.RequestID, rerr.code, rerr.detail)
	}
	return protocol.Message{Type: protocol.TypeSpacePeopleResult, RequestID: msg.RequestID, SpaceResult: result}
}

func (h *Hub) agentSpacePeople(ctx context.Context, req *protocol.SpaceRequest) (*protocol.SpaceResult, *routingError) {
	if h.store == nil {
		return nil, spaceRefusal("spaces are not configured on this broker")
	}
	id := ""
	if req != nil {
		id = req.Space
	}
	pin, rerr := h.pinFor(ctx, id)
	if rerr != nil {
		return nil, rerr
	}
	shown, err := h.visible(pin.Space)
	if err != nil {
		return nil, spaceRefusal(err.Error())
	}
	if people, ok := h.statusCache.get(pin.Space, h.authority.now()); ok {
		return &protocol.SpaceResult{People: peopleView(people, shown), StatusKnown: true}, nil
	}
	res, err := h.mapOnlyPeople(ctx, pin.Space, shown)
	if err != nil {
		return nil, spaceRefusal(err.Error())
	}
	return res, nil
}
