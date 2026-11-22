package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/spaceowner"
	"github.com/pairtask/pairtask/pairbook"
)

// memberList is the people in a space, as its kept map names them.
type memberList []memberView

type memberView struct {
	Name       string   `json:"name"`
	Role       string   `json:"role"`
	Person     string   `json:"person"` // lineage, hex
	Workspaces []string `json:"workspaces"`
}

func (l memberList) String() string {
	var b strings.Builder
	for _, m := range l {
		fmt.Fprintf(&b, "%-20s %-6s %s  %s\n", m.Name, m.Role, m.Person[:12], strings.Join(m.Workspaces, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// pairbookMembers lists the people in the space; it signs nothing.
func pairbookMembers(ctx context.Context, s *pairbookSession, a pairbookArgs) (any, error) {
	pin, err := s.pin(ctx, a.space)
	if err != nil {
		return nil, err
	}
	cur, err := s.current(ctx, pin.Space)
	if err != nil {
		return nil, err
	}
	out := memberList{}
	for _, p := range cur.Map.People {
		out = append(out, memberView{Name: p.Name, Role: p.Role, Person: pairbook.LineageHex(p.Lineage), Workspaces: boundTo(cur.Map, p.Lineage)})
	}
	return out, nil
}

// boundTo are the workspaces the map binds to l.
func boundTo(m pairbook.SignedSpaceMap, l pairbook.Lineage) []string {
	out := []string{}
	for _, b := range m.Bindings {
		if owner, ok := b.ClaimedLineage(); ok && owner == l {
			out = append(out, b.Workspace())
		}
	}
	return out
}

// statusList is each space's state.
type statusList []protocol.SpaceMapStatus

func (l statusList) String() string {
	if len(l) == 0 {
		return "No spaces."
	}
	var b strings.Builder
	for _, st := range l {
		fmt.Fprintf(&b, "%s  %s%s%s\n", st.Space, st.State, untilNote(st), refreshNote(st))
	}
	return strings.TrimRight(b.String(), "\n")
}

func untilNote(st protocol.SpaceMapStatus) string {
	if st.ExpiresAt.IsZero() {
		return ""
	}
	return "  until " + st.ExpiresAt.Local().Format(time.DateTime)
}

func refreshNote(st protocol.SpaceMapStatus) string {
	if st.Refresh == "" {
		return ""
	}
	return "  (last refresh: " + st.Refresh + ")"
}

// pairbookStatus is each space's state; it signs nothing.
func pairbookStatus(ctx context.Context, s *pairbookSession, _ pairbookArgs) (any, error) {
	res, err := s.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapStatus})
	if err != nil {
		return nil, err
	}
	return statusList(res.Maps), nil
}

// pairbookLeave asks the owner to take this person out of the space.
func pairbookLeave(ctx context.Context, s *pairbookSession, a pairbookArgs) (any, error) {
	return s.request(ctx, a.space, spaceowner.Request{Action: pairbook.ChangeLeave})
}

// pairbookUnbind asks the owner to take one of this person's workspaces out.
func pairbookUnbind(ctx context.Context, s *pairbookSession, a pairbookArgs) (any, error) {
	if len(a.words) != 1 {
		return nil, errors.New("unbind needs the WORKSPACE key ID")
	}
	return s.request(ctx, a.space, spaceowner.Request{Action: pairbook.ChangeUnbind, Workspace: a.words[0]})
}

// request signs r against the space's current map and sends it.
func (s *pairbookSession) request(ctx context.Context, id string, r spaceowner.Request) (any, error) {
	pin, err := s.pin(ctx, id)
	if err != nil {
		return nil, err
	}
	me, err := s.person()
	if err != nil {
		return nil, err
	}
	cur, err := s.current(ctx, pin.Space)
	if err != nil {
		return nil, err
	}
	r.Space, r.Base = pin.Space, cur.Mark
	ch := me.Change(r, time.Now())
	if err := s.send(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapSubmit, Space: pin.Space}, ch); err != nil {
		return nil, err
	}
	return done("Sent; it takes effect when the owner next syncs the space."), nil
}
