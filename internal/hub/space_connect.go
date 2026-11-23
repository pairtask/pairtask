package hub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/store"
	"github.com/pairtask/pairtask/pairbook"
)

// Space connects (Pairbook v1): two workspaces that a space's active map
// lets connect become contacts, as a pairing would, with no code. Each side
// decides from its own verified map: the dialer only dials a peer its map
// grants, the responder only accepts one its own map grants, and each
// checks again inside the install. Of a pair, only the lower key ID dials,
// in the lowest space ID that grants them, so both converge on one
// pairing. Nothing is shared: agent access stays a separate step.

// grantedPeer is a workspace this one may connect to: its key and the
// spaces that grant it, sorted.
type grantedPeer struct {
	key    ed25519.PublicKey
	spaces []string
}

// desiredPeers is every workspace an active map lets self connect to.
func (h *Hub) desiredPeers(ctx context.Context, self string) (map[string]grantedPeer, error) {
	pins, err := h.store.SpacePins(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]grantedPeer{}
	now := h.authority.now()
	for _, pin := range pins {
		v, ok := h.SpaceAuthority(pin.Space)
		if !ok {
			continue
		}
		peers, err := v.Peers(self, now)
		if err != nil {
			continue // lapsed between the two reads
		}
		for _, p := range peers {
			g := out[p.Workspace]
			g.key, g.spaces = p.Key, append(g.spaces, pin.Space)
			out[p.Workspace] = g
		}
	}
	for w, g := range out {
		slices.Sort(g.spaces)
		out[w] = g
	}
	return out, nil
}

// grants: space's active map lets self and peer connect now.
func (h *Hub) grants(space, self, peer string) bool {
	v, ok := h.SpaceAuthority(space)
	if !ok {
		return false
	}
	peers, err := v.Peers(self, h.authority.now())
	return err == nil && slices.ContainsFunc(peers, func(p pairbook.Peer) bool { return p.Workspace == peer })
}

// spaceDialer is one connect from this workspace to target.
type spaceDialer struct {
	h      *Hub
	id     *peer.WorkspaceIdentity
	target string
	peer   grantedPeer
}

// connect dials target in the space its kept attempt names, while that
// space still grants it (else the lowest that does), at target's verified
// address, and installs once target accepted and the map still grants.
func (s spaceDialer) connect(ctx context.Context) error {
	spaceID, req, err := s.attempt(ctx)
	if err != nil {
		return err
	}
	pin, ok, err := s.h.store.SpacePin(ctx, spaceID)
	if err != nil || !ok {
		return errors.Join(err, errors.New("space "+spaceID+" isn't pinned here"))
	}
	dir, err := s.h.mapDirectory(pin.Base)
	if err != nil {
		return err
	}
	addr, err := dir.MapAddress(ctx, pin, s.peer.key)
	if errors.Is(err, space.ErrNoAddress) {
		return errUndialable // not now: the next refresh tries again
	}
	if err != nil {
		return err
	}
	reached, err := s.knock(ctx, addr.Addrs, req)
	if err != nil {
		return s.forgetEnded(ctx, err)
	}
	return s.install(ctx, req, reached)
}

// attempt is the one attempt kept for target, made in the lowest granting
// space when none is kept, and made again when its space stopped granting.
func (s spaceDialer) attempt(ctx context.Context) (string, space.ConnectRequest, error) {
	var fresh [16]byte
	if _, err := rand.Read(fresh[:]); err != nil {
		return "", space.ConnectRequest{}, err
	}
	kept, id, err := s.h.store.SpaceAttempt(ctx, s.target, s.peer.spaces[0], fresh)
	if err != nil {
		return "", space.ConnectRequest{}, err
	}
	if !slices.Contains(s.peer.spaces, kept) {
		if err := s.h.store.EndSpaceAttempt(ctx, s.target); err != nil {
			return "", space.ConnectRequest{}, err
		}
		return s.attempt(ctx)
	}
	return kept, space.ConnectRequest{Space: kept, ConnectID: id}, nil
}

// forgetEnded forgets the attempt when its pairing definitively ended, so
// the next connect begins a new one; any other failure keeps it.
func (s spaceDialer) forgetEnded(ctx context.Context, err error) error {
	if !errors.Is(err, errRetiredAttempt) {
		return err
	}
	return errors.Join(err, s.h.store.EndSpaceAttempt(ctx, s.target))
}

// knock tries each address in turn and returns the one whose workspace,
// presenting the target's key, accepted.
func (s spaceDialer) knock(ctx context.Context, addrs []string, req space.ConnectRequest) (string, error) {
	err := errors.New("no address answered")
	for _, addr := range addrs {
		var answer space.ConnectAnswer
		if answer, err = s.ask(ctx, addr, req); err != nil {
			continue
		}
		switch {
		case answer.Ended:
			return "", errRetiredAttempt
		case !answer.Accepted:
			return "", fmt.Errorf("%s refused: %s", s.target, answer.Reason)
		}
		return addr, nil
	}
	return "", err
}

// ask sends req over a space connection pinned to the target at addr.
func (s spaceDialer) ask(ctx context.Context, addr string, req space.ConnectRequest) (space.ConnectAnswer, error) {
	raw, err := s.h.pair.cfg.Dial(ctx, addr)
	if err != nil {
		return space.ConnectAnswer{}, err
	}
	conn, err := firstcontact.SpaceClient(ctx, raw, s.id, s.h.pair.cfg.Clock, s.target)
	if err != nil {
		return space.ConnectAnswer{}, err
	}
	defer conn.Close()
	frames := conn.Frames(ctx)
	if err := frames.Send(req.Encode()); err != nil {
		return space.ConnectAnswer{}, err
	}
	frame, err := frames.Recv()
	if err != nil {
		return space.ConnectAnswer{}, err
	}
	return space.DecodeConnectAnswer(frame)
}

// install keeps the accepted pairing as the side that dialed, if the map
// still grants target, then ends the attempt.
func (s spaceDialer) install(ctx context.Context, req space.ConnectRequest, reached string) error {
	now := time.Now()
	contact := peer.Contact{IdentityKey: s.target, Destination: reached, DestinationAt: now, EstablishedAt: now, Joined: true}
	err := s.h.installGranted(ctx, spaceInstall{contact: contact, req: req, own: s.id.Fingerprint(), role: "joiner"})
	if err != nil {
		return s.forgetEnded(ctx, err)
	}
	return s.h.store.EndSpaceAttempt(ctx, s.target)
}

// spaceInstall is one side's install of a connect: the contact it makes,
// the request, this workspace's key ID and its role.
type spaceInstall struct {
	contact peer.Contact
	req     space.ConnectRequest
	own     string
	role    string
}

// errNotGranted: the space's active map no longer lets the two connect.
var errNotGranted = errors.New("the space no longer lets these workspaces connect")

// installGranted installs in's pairing under the space's writer lock, so
// no map change interleaves, once its active map still grants the peer;
// the store refuses a workspace the operator refused, in the same
// transaction that marks the contact managed.
func (h *Hub) installGranted(ctx context.Context, in spaceInstall) error {
	unlock := h.authority.lockSpace(spaceKey(in.req.Space))
	defer unlock()
	h.authority.contactsMu.Lock()
	defer h.authority.contactsMu.Unlock()
	if !h.grants(in.req.Space, in.own, in.contact.IdentityKey) {
		return errNotGranted
	}
	return installSpace(ctx, h, in)
}

// installSpace installs in's pairing, or finds it installed by an earlier
// try of the same attempt. One the ledger has since retired is no pairing.
func installSpace(ctx context.Context, h *Hub, in spaceInstall) error {
	id, err := space.PairingID(in.req.Space, in.own, in.contact.IdentityKey, in.req.ConnectID)
	if err != nil {
		return err
	}
	record := peer.SpaceInstall{Contact: in.contact, Space: in.req.Space, ConnectID: in.req.ConnectID, PairingID: id, Role: in.role}
	result, err := h.installRecord(ctx, in, record)
	if errors.Is(err, store.ErrSpaceSuperseded) {
		return nil // the peer's dial already paired us, and both ends keep that pairing
	}
	if err != nil {
		return err
	}
	if result.Exchange.State != peer.ExchangeInstalled {
		return errRetiredAttempt
	}
	h.PairingChanged()
	return nil
}

// installRecord installs a responder's connect as before; a dialer's goes
// through the crossing rule, where the lower key's dial wins.
func (h *Hub) installRecord(ctx context.Context, in spaceInstall, record peer.SpaceInstall) (peer.InstallResult, error) {
	if in.role != "joiner" {
		return h.store.InstallSpaceContact(ctx, record)
	}
	return h.store.InstallSpaceDial(ctx, record, in.own < in.contact.IdentityKey)
}

// errRetiredAttempt is an attempt whose pairing was installed, then ended.
var errRetiredAttempt = errors.New("that connect attempt's pairing has since ended")

// spaceResponder answers inbound connects as the workspace id.
type spaceResponder struct {
	h  *Hub
	id *peer.WorkspaceIdentity
}

// pins admits a connect's handshake while spaces are configured: who the
// dialer is decides nothing until the map is checked.
func (r spaceResponder) pins(string) bool { return r.h.spaces != nil }

// answer accepts a connect the map grants, or refuses it and why; a
// refusal writes nothing.
func (r spaceResponder) answer(ctx context.Context, peerLeaf string, req space.ConnectRequest) space.ConnectAnswer {
	err := r.accept(ctx, peerLeaf, req)
	switch {
	case err == nil:
		return space.ConnectAnswer{Accepted: true}
	case errors.Is(err, errRetiredAttempt):
		return space.ConnectAnswer{Ended: true} // only for this attempt's own retired pairing
	}
	return space.ConnectAnswer{Reason: refusalReason(err)}
}

// accept installs the pairing as the side that answered, if this side's
// own active map lets the dialer connect, and the dialer is the side the
// rule says dials: the lower key ID, or a dialer that can't be dialed in
// the request's space because it publishes no address there.
func (r spaceResponder) accept(ctx context.Context, peerLeaf string, req space.ConnectRequest) error {
	own := r.id.Fingerprint()
	if r.h.spaces == nil || r.h.store == nil {
		return errors.New("spaces are not configured")
	}
	if err := r.dialerMayDial(ctx, peerLeaf, own, req.Space); err != nil {
		return err
	}
	contact := peer.Contact{IdentityKey: peerLeaf, EstablishedAt: time.Now(), Invited: true}
	return r.h.installGranted(ctx, spaceInstall{contact: contact, req: req, own: own, role: "inviter"})
}

var (
	errSelfDial   = errors.New("a workspace can't connect to itself")
	errOtherDials = errors.New("the other workspace dials")
	errUndialable = errors.New("the workspace publishes no address yet")
	errNotSettled = errors.New("can't tell yet whether the dialer can be reached; try later")
	errCrossing   = errors.New("both sides are dialing; try later")
)

// dialerMayDial: peer is the side that dials. A lower key always is. A
// higher key is only when its grant here holds and the Pairbook itself says
// it publishes no address in space; any doubt (a timeout, a refusal, an
// unreadable answer) refuses for now.
func (r spaceResponder) dialerMayDial(ctx context.Context, peerLeaf, own, spaceID string) error {
	switch {
	case peerLeaf == own:
		return errSelfDial
	case peerLeaf < own:
		return nil
	}
	key, ok := r.h.grantedKey(spaceID, own, peerLeaf)
	if !ok {
		return errNotGranted
	}
	if r.h.dialingTo(ctx, peerLeaf) {
		return errCrossing // this side is dialing it: one of the two must win, and the lower key's dial does
	}
	_, err := r.h.addressOf(ctx, spaceID, key)
	switch {
	case errors.Is(err, space.ErrNoAddress):
		return nil
	case err == nil:
		return errOtherDials
	}
	return errNotSettled
}

// grantedKey is peer's key, if space's active map lets self and peer
// connect now.
func (h *Hub) grantedKey(spaceID, self, peerLeaf string) (ed25519.PublicKey, bool) {
	v, ok := h.SpaceAuthority(spaceID)
	if !ok {
		return nil, false
	}
	peers, err := v.Peers(self, h.authority.now())
	if err != nil {
		return nil, false
	}
	for _, p := range peers {
		if p.Workspace == peerLeaf {
			return p.Key, true
		}
	}
	return nil, false
}

// addressOf is node's address record in space, from its Pairbook.
func (h *Hub) addressOf(ctx context.Context, spaceID string, node ed25519.PublicKey) (pairbook.SignedAddress, error) {
	pin, ok, err := h.store.SpacePin(ctx, spaceID)
	if err != nil || !ok {
		return pairbook.SignedAddress{}, errors.Join(err, errNotSettled)
	}
	dir, err := h.mapDirectory(pin.Base)
	if err != nil {
		return pairbook.SignedAddress{}, err
	}
	return dir.MapAddress(ctx, pin, node)
}

// refusalReason is what a dialer is told, without internals.
func refusalReason(err error) string {
	switch {
	case errors.Is(err, errNotGranted):
		return "not granted by this space"
	case errors.Is(err, store.ErrSpaceRefused):
		return "refused by the operator"
	case errors.Is(err, peer.ErrSpaceConflict):
		return "already paired with this workspace"
	}
	return "could not keep the contact"
}
