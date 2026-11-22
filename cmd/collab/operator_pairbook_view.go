package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/enroll"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/spaceowner"
	"github.com/pairtask/pairtask/pairbook"
)

// memberList is the people in a space, as its kept map names them.
// memberList is the people this workspace may see in a space. Each keeps
// its workspaces' key IDs, as before; Status adds their online state and
// listed agents when the Pairbook keeps status (absent otherwise).
type memberList []memberView

type memberView struct {
	Name       string                    `json:"name"`
	Role       string                    `json:"role"`
	Person     string                    `json:"person"` // lineage, hex
	Workspaces []string                  `json:"workspaces"`
	Status     []protocol.SpaceWorkspace `json:"status,omitempty"`
}

func (l memberList) String() string {
	var b strings.Builder
	for _, m := range l {
		fmt.Fprintf(&b, "%-20s %-6s %s\n", m.Name, m.Role, m.Person[:12])
		for _, w := range m.statusOf() {
			fmt.Fprintf(&b, "  %s  %s%s\n", enroll.Fingerprint(w.Workspace), presence(w, m.Status != nil), agentNote(w.Agents))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// statusOf is the member's workspaces with status, or bare when unknown.
func (m memberView) statusOf() []protocol.SpaceWorkspace {
	if m.Status != nil {
		return m.Status
	}
	out := make([]protocol.SpaceWorkspace, 0, len(m.Workspaces))
	for _, w := range m.Workspaces {
		out = append(out, protocol.SpaceWorkspace{Workspace: w})
	}
	return out
}

// presence is a workspace's online state, or unknown when the Pairbook
// keeps none.
func presence(w protocol.SpaceWorkspace, known bool) string {
	switch {
	case !known:
		return "status unknown"
	case w.Online:
		return "online"
	case w.LastSeen.IsZero():
		return "not seen"
	}
	return "last seen " + w.LastSeen.Local().Format("2006-01-02 15:04")
}

func agentNote(agents []protocol.SpaceAgent) string {
	if len(agents) == 0 {
		return ""
	}
	names := make([]string, 0, len(agents))
	for _, a := range agents {
		names = append(names, a.Name)
	}
	return "  agents: " + strings.Join(names, ", ")
}

// pairbookMembers lists the people this workspace may see in the space,
// online and with their listed agents; it signs nothing.
func pairbookMembers(ctx context.Context, s *pairbookSession, a pairbookArgs) (any, error) {
	pin, err := s.pin(ctx, a.space)
	if err != nil {
		return nil, err
	}
	res, err := s.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapPeople, Space: pin.Space})
	if err != nil {
		return nil, err
	}
	return membersOf(res), nil
}

func membersOf(res *protocol.SpaceResult) memberList {
	out := memberList{}
	for _, p := range res.People {
		m := memberView{Name: p.Name, Role: p.Role, Person: p.Person, Workspaces: []string{}}
		for _, w := range p.Workspaces {
			m.Workspaces = append(m.Workspaces, w.Workspace)
		}
		if res.StatusKnown {
			m.Status = append([]protocol.SpaceWorkspace{}, p.Workspaces...)
		}
		out = append(out, m)
	}
	return out
}

// pairbookShare lists an agent in the space; pairbookUnshare stops.
func pairbookShare(ctx context.Context, s *pairbookSession, a pairbookArgs) (any, error) {
	return pairbookListing(ctx, s, a, protocol.SpaceOpMapShare)
}

func pairbookUnshare(ctx context.Context, s *pairbookSession, a pairbookArgs) (any, error) {
	return pairbookListing(ctx, s, a, protocol.SpaceOpMapUnshare)
}

func pairbookListing(ctx context.Context, s *pairbookSession, a pairbookArgs, op string) (any, error) {
	if len(a.words) != 1 {
		return nil, errors.New("name one AGENT")
	}
	pin, err := s.pin(ctx, a.space)
	if err != nil {
		return nil, err
	}
	res, err := s.space(ctx, protocol.SpaceRequest{Op: op, Space: pin.Space, Agent: a.words[0]})
	if err != nil {
		return nil, err
	}
	listed := "none"
	if len(res.Shares) > 0 {
		listed = strings.Join(res.Shares, ", ")
	}
	return done("Listed in " + pin.Space + ": " + listed + ". Listing advertises agents; who may message them is still your contact sharing."), nil
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

// pairbookPublish names the addresses other members reach this workspace
// at; the broker signs and renews the record.
func pairbookPublish(ctx context.Context, s *pairbookSession, a pairbookArgs) (any, error) {
	if len(a.words) == 0 {
		return nil, errors.New("publish needs at least one HOST:PORT")
	}
	if a.addressValid < time.Millisecond || a.addressValid > pairbook.AddressValidity {
		return nil, fmt.Errorf("--valid must be between 1ms and %s", pairbook.AddressValidity)
	}
	pin, err := s.pin(ctx, a.space)
	if err != nil {
		return nil, err
	}
	r := protocol.SpaceRequest{Op: protocol.SpaceOpMapAddress, Space: pin.Space, Addresses: a.words, ValidFor: a.addressValid.Milliseconds()}
	if _, err := s.space(ctx, r); err != nil {
		return nil, err
	}
	return done("Published; renewed while this workspace is in the space."), nil
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
