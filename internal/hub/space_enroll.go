package hub

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
)

// Enrolling this workspace in an organization space. The broker pins the
// space from the join link, starts the enrollment and sends the person's
// join as the host signed it; the person confirms in a browser. Once the
// Pairbook admits it, the next refresh finds this workspace in the map.

func init() {
	spaceOps[protocol.SpaceOpMapEnroll] = (*Hub).mapEnroll
	spaceOps[protocol.SpaceOpMapEnrollSend] = (*Hub).mapEnrollSend
	spaceOps[protocol.SpaceOpMapEnrollState] = (*Hub).mapEnrollState
}

var errNoEnroll = errors.New("this Pairbook doesn't enroll workspaces")

// mapEnroll pins the link's space (pending until a map binds this
// workspace) and starts an enrollment as r.Name.
func (h *Hub) mapEnroll(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	link, err := space.ParseOrgLink(r.Link)
	if err != nil {
		return spaceStep{}, spaceRefusal("not an organization join link")
	}
	if !pairbook.ValidName(r.Name) {
		return spaceStep{}, spaceRefusal("a name in a space is 1 to 64 bytes with no control characters")
	}
	pinned, rerr := h.linkPinned(ctx, link.Invite())
	if rerr != nil {
		return spaceStep{}, rerr
	}
	pin := space.Pin{Space: link.Space, Owner: link.Owner, Base: link.Base, JoinedAt: time.Now().UTC()}
	return spaceStep{space: pin.Space, job: func(net, local context.Context) (*protocol.SpaceResult, error) {
		if !pinned {
			if err := h.store.PinSpace(local, pin); err != nil {
				return nil, err
			}
		}
		return h.startEnroll(net, pin, r.Name)
	}}, nil
}

func (h *Hub) startEnroll(ctx context.Context, pin space.Pin, name string) (*protocol.SpaceResult, error) {
	enr, err := h.enroller(pin)
	if err != nil {
		return nil, err
	}
	e, err := enr.StartEnroll(ctx, pin, name)
	if err != nil {
		return nil, err
	}
	return &protocol.SpaceResult{Enroll: &protocol.SpaceEnroll{Space: pin.Space, Enrollment: e.ID, Challenge: e.Challenge, Base: *markView(e.Base),
		Code: e.Code, URL: e.URL, ExpiresAt: time.UnixMilli(e.ExpiresAt).UTC()}}, nil
}

func (h *Hub) enroller(pin space.Pin) (space.Enroller, error) {
	dir, err := h.mapDirectory(pin.Base)
	if err != nil {
		return nil, err
	}
	enr, ok := dir.(space.Enroller)
	if !ok {
		return nil, errNoEnroll
	}
	return enr, nil
}

// mapEnrollSend sends the person's join for an enrollment of a pinned
// space: a join of that space, binding this workspace.
func (h *Hub) mapEnrollSend(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	var ch pairbook.SignedChange
	if rerr := decodeRecord(r.Record, &ch); rerr != nil {
		return spaceStep{}, rerr
	}
	pin, rerr := h.enrollPin(ctx, r)
	if rerr != nil {
		return spaceStep{}, rerr
	}
	if !joinOf(ch, pin.Space) {
		return spaceStep{}, spaceRefusal("the request isn't a join of this space")
	}
	return spaceStep{space: pin.Space, job: func(net, _ context.Context) (*protocol.SpaceResult, error) {
		enr, err := h.enroller(pin)
		if err != nil {
			return nil, err
		}
		st, err := enr.SubmitEnroll(net, pin, r.Enrollment, ch)
		return h.enrollResult(st, err)
	}}, nil
}

// mapEnrollState is an enrollment's state now.
func (h *Hub) mapEnrollState(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	pin, rerr := h.enrollPin(ctx, r)
	if rerr != nil {
		return spaceStep{}, rerr
	}
	return spaceStep{space: pin.Space, job: func(net, _ context.Context) (*protocol.SpaceResult, error) {
		enr, err := h.enroller(pin)
		if err != nil {
			return nil, err
		}
		st, err := enr.EnrollState(net, pin, r.Enrollment)
		return h.enrollResult(st, err)
	}}, nil
}

// joinOf: ch is a join of space.
func joinOf(ch pairbook.SignedChange, space string) bool {
	return ch.Action == pairbook.ChangeJoin && ch.Space == space
}

func (h *Hub) enrollPin(ctx context.Context, r protocol.SpaceRequest) (space.Pin, *routingError) {
	if !validEnrollment(r.Enrollment) {
		return space.Pin{}, spaceRefusal("not an enrollment ID")
	}
	return h.pinFor(ctx, r.Space)
}

func validEnrollment(id string) bool {
	return len(id) == 32 && strings.Trim(id, "0123456789abcdef") == ""
}

// enrollResult reports a state, and asks for a refresh once admitted so
// the new map, and the connections it allows, come at once.
func (h *Hub) enrollResult(st space.EnrollState, err error) (*protocol.SpaceResult, error) {
	if err != nil {
		return nil, err
	}
	if st.State == space.EnrollAdmitted {
		h.kickRefresh()
	}
	out := &protocol.SpaceEnrollState{State: st.State}
	if st.Base != nil {
		out.Base = markView(*st.Base)
	}
	return &protocol.SpaceResult{EnrollState: out}, nil
}
