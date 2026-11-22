package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/spaceowner"
	"github.com/pairtask/pairtask/pairbook"
)

// pin is the pinned space id names, or the only one when id is empty.
func (s *pairbookSession) pin(ctx context.Context, id string) (protocol.SpacePin, error) {
	res, err := s.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpList})
	if err != nil {
		return protocol.SpacePin{}, err
	}
	for _, p := range res.Pins {
		if chosen(p, id, len(res.Pins)) {
			return p, nil
		}
	}
	return protocol.SpacePin{}, errors.New("name the space with --space (pinned: " + pinList(res.Pins) + ")")
}

// chosen: id names p, or no id was given and p is the only pin.
func chosen(p protocol.SpacePin, id string, pins int) bool {
	if id == "" {
		return pins == 1
	}
	return p.Space == id
}

func pinList(pins []protocol.SpacePin) string {
	ids := make([]string, 0, len(pins))
	for _, p := range pins {
		ids = append(ids, p.Space)
	}
	return strings.Join(ids, ", ")
}

// inviteLink is a printed invite: the link holds the one-time secret.
type inviteLink struct{ Link string }

func (l inviteLink) String() string {
	return "Invite link (one-time; send it privately):\n  " + l.Link
}

// pairbookInvite signs a one-time invite to the space and records it.
func pairbookInvite(ctx context.Context, s *pairbookSession, a pairbookArgs) (any, error) {
	pin, err := s.pin(ctx, a.space)
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
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	inv, err := me.Invite(cur, sha256.Sum256(token[:]), time.Now(), a.valid)
	if err != nil {
		return nil, err
	}
	if err := s.send(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapInvite, Space: pin.Space}, inv); err != nil {
		return nil, err
	}
	return inviteLink{space.Invite{Base: pin.Directory, Space: pin.Space, Token: token, Owner: pin.Owner}.String()}, nil
}

// send is a request carrying record.
func (s *pairbookSession) send(ctx context.Context, r protocol.SpaceRequest, record any) error {
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	r.Record = body
	_, err = s.space(ctx, r)
	return err
}

// done is a command's plain answer.
type done string

func (d done) String() string { return string(d) }

// pairbookJoin asks to join the space a link invites to, binding this
// workspace.
func pairbookJoin(ctx context.Context, s *pairbookSession, a pairbookArgs) (any, error) {
	if len(a.words) != 1 {
		return nil, errors.New("join needs the invite LINK")
	}
	name, err := joinName(a.name)
	if err != nil {
		return nil, err
	}
	inv, err := s.fetchInvite(ctx, a.words[0])
	if err != nil {
		return nil, err
	}
	me, err := s.person()
	if err != nil {
		return nil, err
	}
	now := time.Now().Truncate(time.Millisecond)
	own, err := s.binding(ctx, me, now)
	if err != nil {
		return nil, err
	}
	ch := me.Change(spaceowner.Request{Space: inv.Space, Action: pairbook.ChangeJoin, Base: inv.Base, Name: name, TokenHash: inv.TokenHash, Binding: own}, now)
	r := protocol.SpaceRequest{Op: protocol.SpaceOpMapSubmit, Link: a.words[0], Until: time.UnixMilli(inv.ExpiresAt)}
	if err := s.send(ctx, r, ch); err != nil {
		return nil, err
	}
	return done("Asked to join space " + inv.Space + " as " + name + ". You're in once the owner approves: pairtask space status shows it."), nil
}

// joinName is the name a join carries: --name, or this machine's host name
// when none is given; refused before anything is sent if not valid.
func joinName(given string) (string, error) {
	name := given
	if name == "" {
		host, err := os.Hostname()
		if err != nil {
			return "", errors.New("no --name given and no host name to use; join with --name NAME")
		}
		name = strings.TrimSuffix(host, ".local")
	}
	if !pairbook.ValidName(name) {
		return "", fmt.Errorf("%q can't be a name in a space: 1 to %d bytes, no control characters", name, pairbook.MaxName)
	}
	return name, nil
}

func (s *pairbookSession) fetchInvite(ctx context.Context, link string) (pairbook.SignedMapInvite, error) {
	res, err := s.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapInviteFetch, Link: link})
	if err != nil {
		return pairbook.SignedMapInvite{}, err
	}
	var inv pairbook.SignedMapInvite
	return inv, json.Unmarshal(res.Record, &inv)
}

// pending is the directory's waiting requests with each join's invite
// state.
type pending struct {
	changes []pairbook.SignedChange
	states  []string
}

func (s *pairbookSession) pending(ctx context.Context, id string) (pending, error) {
	res, err := s.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapChanges, Space: id})
	if err != nil {
		return pending{}, err
	}
	p := pending{states: res.Invites}
	for _, raw := range res.Changes {
		var c pairbook.SignedChange
		if err := json.Unmarshal(raw, &c); err != nil {
			return pending{}, err
		}
		p.changes = append(p.changes, c)
	}
	if len(p.states) != len(p.changes) {
		return pending{}, errors.New("the broker's requests and invite states disagree")
	}
	return p, nil
}

// requestList is printed requests.
type requestList []string

func (l requestList) String() string {
	if len(l) == 0 {
		return "No requests waiting."
	}
	return strings.Join(l, "\n")
}

// pairbookRequests lists the waiting requests.
func pairbookRequests(ctx context.Context, s *pairbookSession, a pairbookArgs) (any, error) {
	pin, err := s.pin(ctx, a.space)
	if err != nil {
		return nil, err
	}
	p, err := s.pending(ctx, pin.Space)
	if err != nil {
		return nil, err
	}
	out := requestList{}
	for i, c := range p.changes {
		out = append(out, describeChange(c, p.states[i]))
	}
	return out, nil
}

func describeChange(c pairbook.SignedChange, state string) string {
	d := c.Digest()
	line := fmt.Sprintf("%s  %-6s person %s", hex.EncodeToString(d[:6]), c.Action, pairbook.LineageHex(c.Lineage)[:12])
	if c.Action == pairbook.ChangeJoin {
		line += fmt.Sprintf("  name %q  invite %s", c.Name, state)
	}
	return line
}
