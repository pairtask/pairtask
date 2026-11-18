package hub

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
)

// A space connect (Pairbook) makes two members of a space contacts, as a
// pairing would, without a code: each side checks, against its own pin of
// the space's owner, that both are members now, and both install the same
// epoch pairing, whose ID they derive from the space, their keys and the
// attempt. Nothing is shared: agent access stays a separate step.

// spaceConnect connects to the member r.Node names, under the space's
// change slot, which it holds through verification and install.
func (h *Hub) spaceConnect(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	if r.Node == "" {
		return spaceStep{}, &routingError{protocol.ErrMalformedFrame, "connect needs the member's key ID"}
	}
	if r.Node == h.identity.Fingerprint() {
		return spaceStep{}, spaceRefusal("a workspace can't connect to itself")
	}
	p, d, rerr := h.pinned(ctx, r.Space)
	if rerr != nil {
		return spaceStep{}, rerr
	}
	dialer := spaceDialer{h: h, id: h.identity, pin: p, dir: d, target: r.Node}
	return spaceStep{space: p.Space, job: dialer.connect}, nil
}

// spaceDialer is one connect from this workspace, id, to target in pin's
// space through its directory.
type spaceDialer struct {
	h      *Hub
	id     *peer.WorkspaceIdentity
	pin    space.Pin
	dir    space.Directory
	target string
}

// connect verifies both memberships, answers a contact already paired as
// it is, and otherwise knocks at the target's verified addresses with the
// attempt kept for this target, installing once the target accepted.
func (s spaceDialer) connect(net, local context.Context) (*protocol.SpaceResult, error) {
	member, err := memberCheck{s.dir, s.pin, s.id.Fingerprint()}.verify(net, s.target)
	if err != nil {
		return nil, err
	}
	if ok, err := s.h.paired(local, s.target); ok || err != nil {
		return contactResult(s.target), err
	}
	if !s.h.canDial() {
		return nil, errors.New("this broker can't dial other workspaces")
	}
	req, err := s.attempt(local)
	if err != nil {
		return nil, err
	}
	addr, err := s.dir.Address(net, s.pin, member.Node)
	if err != nil {
		return nil, err
	}
	reached, err := s.knock(net, addr.Addrs, req)
	if err != nil {
		return nil, err
	}
	return s.install(local, req, member.Name, reached)
}

// attempt is this connect's request, its ID kept before any dial so a
// retry, even after a restart, repeats it.
func (s spaceDialer) attempt(local context.Context) (space.ConnectRequest, error) {
	var fresh [16]byte
	if _, err := rand.Read(fresh[:]); err != nil {
		return space.ConnectRequest{}, err
	}
	id, err := s.h.store.SpaceAttempt(local, s.pin.Space, s.target, fresh)
	return space.ConnectRequest{Space: s.pin.Space, ConnectID: id}, err
}

// knock tries each address in turn and returns the one whose workspace,
// presenting the target's key, accepted.
func (s spaceDialer) knock(net context.Context, addrs []string, req space.ConnectRequest) (string, error) {
	err := errors.New("no address answered")
	for _, addr := range addrs {
		var answer space.ConnectAnswer
		if answer, err = s.ask(net, addr, req); err != nil {
			continue
		}
		if !answer.Accepted {
			return "", fmt.Errorf("%s refused: %s", s.target, answer.Reason)
		}
		return addr, nil
	}
	return "", err
}

// ask sends req over a space connection pinned to the target at addr.
func (s spaceDialer) ask(net context.Context, addr string, req space.ConnectRequest) (space.ConnectAnswer, error) {
	raw, err := s.h.pair.cfg.Dial(net, addr)
	if err != nil {
		return space.ConnectAnswer{}, err
	}
	conn, err := firstcontact.SpaceClient(net, raw, s.id, s.h.pair.cfg.Clock, s.target)
	if err != nil {
		return space.ConnectAnswer{}, err
	}
	defer conn.Close()
	frames := conn.Frames(net)
	if err := frames.Send(req.Encode()); err != nil {
		return space.ConnectAnswer{}, err
	}
	frame, err := frames.Recv()
	if err != nil {
		return space.ConnectAnswer{}, err
	}
	return space.DecodeConnectAnswer(frame)
}

// install keeps the accepted pairing as the side that dialed, then ends
// the attempt.
func (s spaceDialer) install(local context.Context, req space.ConnectRequest, name, reached string) (*protocol.SpaceResult, error) {
	now := time.Now()
	contact := peer.Contact{IdentityKey: s.target, Name: name, Destination: reached, DestinationAt: now, EstablishedAt: now, Joined: true}
	if err := installSpace(local, s.h, spaceInstall{contact: contact, req: req, own: s.id.Fingerprint(), role: "joiner"}); err != nil {
		return nil, err
	}
	if err := s.h.store.EndSpaceAttempt(local, s.pin.Space, s.target); err != nil {
		return nil, err
	}
	return contactResult(s.target), nil
}

// contactResult names the contact a connect answers with; the answer's
// full view is made on the Run goroutine (contactAnswer).
func contactResult(key string) *protocol.SpaceResult {
	return &protocol.SpaceResult{Contact: &protocol.PairingContactView{IdentityKey: key}}
}

// spaceInstall is one side's install of a connect: the contact it makes,
// the request, this workspace's key ID and its role.
type spaceInstall struct {
	contact peer.Contact
	req     space.ConnectRequest
	own     string
	role    string
}

// installSpace installs in's pairing, or finds it installed by an earlier
// try of the same attempt. One the ledger has since retired (an unpair
// after it) is no pairing: the store answers its history without a write,
// and the connect fails rather than report a contact that isn't one.
func installSpace(ctx context.Context, h *Hub, in spaceInstall) error {
	id, err := space.PairingID(in.req.Space, in.own, in.contact.IdentityKey, in.req.ConnectID)
	if err != nil {
		return err
	}
	result, err := h.store.InstallSpaceContact(ctx, peer.SpaceInstall{Contact: in.contact, Space: in.req.Space, ConnectID: in.req.ConnectID, PairingID: id, Role: in.role})
	if err != nil {
		return err
	}
	if result.Exchange.State != peer.ExchangeInstalled {
		return errRetiredAttempt
	}
	h.PairingChanged()
	return nil
}

// errRetiredAttempt is an attempt whose pairing was installed, then ended.
var errRetiredAttempt = errors.New("that connect attempt's pairing has since ended; connect again")

// memberCheck reads a space's members from its directory under pin, as
// self sees them.
type memberCheck struct {
	dir  space.Directory
	pin  space.Pin
	self string
}

// verify is the member target is, once the directory shows both self and
// target as members the owner vouches for now.
func (m memberCheck) verify(ctx context.Context, target string) (pairbook.Member, error) {
	members, err := m.dir.Members(ctx, m.pin)
	if err != nil {
		return pairbook.Member{}, err
	}
	if !slices.ContainsFunc(members, keyed(m.self)) {
		return pairbook.Member{}, errors.New("this workspace is not a verified member of space " + m.pin.Space)
	}
	i := slices.IndexFunc(members, keyed(target))
	if i < 0 {
		return pairbook.Member{}, errors.New(target + " is not a verified member of space " + m.pin.Space)
	}
	return members[i], nil
}

func keyed(id string) func(pairbook.Member) bool {
	return func(m pairbook.Member) bool { return pairbook.KeyID(m.Node) == id }
}

// paired: a contact with key is already paired and confirmed.
func (h *Hub) paired(ctx context.Context, key string) (bool, error) {
	if _, found, err := h.store.Contact(ctx, key); err != nil || !found {
		return false, err
	}
	return h.store.PeerMessages().Confirmed(ctx, key)
}

// contactAnswer fills a connect's result with its contact's full view, as
// the pairing snapshot shows it; it runs on the Run goroutine.
func (h *Hub) contactAnswer(ctx context.Context, result *protocol.SpaceResult) error {
	if result == nil || result.Contact == nil {
		return nil
	}
	key := result.Contact.IdentityKey
	c, found, err := h.store.Contact(ctx, key)
	if err != nil || !found {
		return errors.Join(err, errors.New("the contact is gone"))
	}
	confirmed, err := h.store.PeerMessages().Confirmed(ctx, key)
	if err != nil {
		return err
	}
	counts, err := h.contactCounts(ctx)
	if err != nil {
		return err
	}
	view := h.contactView(c, confirmed, counts, h.identity.Fingerprint())
	result.Contact = &view
	return nil
}

// spaceResponder answers inbound connects as the workspace id.
type spaceResponder struct {
	h  *Hub
	id *peer.WorkspaceIdentity
}

// pins admits a connect's handshake while spaces are configured: who the
// dialer is decides nothing until its membership is checked.
func (r spaceResponder) pins(string) bool { return r.h.spaces != nil }

// answer accepts a member's connect, or refuses it and why; a refusal
// writes nothing.
func (r spaceResponder) answer(ctx context.Context, peerLeaf string, req space.ConnectRequest) space.ConnectAnswer {
	if err := r.accept(ctx, peerLeaf, req); err != nil {
		return space.ConnectAnswer{Reason: err.Error()}
	}
	return space.ConnectAnswer{Accepted: true}
}

// accept checks, under this side's pin and the space's change slot, that
// both are members now, then installs the pairing as the side that
// answered. A retry of an accepted attempt installs nothing new.
func (r spaceResponder) accept(ctx context.Context, peerLeaf string, req space.ConnectRequest) error {
	if r.h.spaces == nil {
		return errors.New("spaces are not configured")
	}
	if !r.h.spaceBusy.hold(req.Space) {
		return errors.New("another change to the space is running; try again")
	}
	defer r.h.spaceBusy.release(req.Space)
	pin, ok, err := r.h.store.SpacePin(ctx, req.Space)
	if err != nil || !ok {
		return errors.New("not a member of that space")
	}
	member, err := r.verify(ctx, pin, peerLeaf)
	if err != nil {
		return err
	}
	contact := peer.Contact{IdentityKey: peerLeaf, Name: member.Name, EstablishedAt: time.Now(), Invited: true}
	return installRefusal(installSpace(ctx, r.h, spaceInstall{contact: contact, req: req, own: r.id.Fingerprint(), role: "inviter"}))
}

// verify is the dialer's membership, checked with this side's own directory
// client under its pin, with its own membership too.
func (r spaceResponder) verify(ctx context.Context, pin space.Pin, dialer string) (pairbook.Member, error) {
	d, err := r.h.spaces.Directory(pin.Base, r.id)
	if err != nil {
		return pairbook.Member{}, errors.New("directory unavailable")
	}
	member, err := memberCheck{d, pin, r.id.Fingerprint()}.verify(ctx, dialer)
	if err != nil {
		return pairbook.Member{}, errors.New("membership not verified")
	}
	return member, nil
}

// installRefusal is what a dialer is told about a failed install: a
// conflict as such, anything else without internals.
func installRefusal(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, peer.ErrSpaceConflict):
		return errors.New("already paired with this workspace")
	case errors.Is(err, errRetiredAttempt):
		return err
	}
	return errors.New("could not keep the contact")
}
