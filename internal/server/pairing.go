package server

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/peer/shared"
)

// Inbound admission: unauthenticated handshakes have their own bound, and
// only then does a connection take a pairing slot or a message slot by its
// negotiated protocol, so neither kind can starve the other. Anything over a
// bound is closed at once. The invitation itself admits one attempt at a
// time, and a contact one message session (#210).
const (
	maxHandshakes   = 8
	maxPairingConns = 4
	maxMessageConns = 4
)

// PairingServer accepts first-contact pairing connections (#178) on a Unix
// socket, typically forwarded by a relay. Its directory must be private to
// this user; the socket is 0600. Like the agent and operator sockets, an
// existing path is never removed: an operator deletes a stale one.
type PairingServer struct {
	*Server
	handshakes, slots, messages chan struct{}
}

// NewPairing creates a pairing server bound to socketPath (not yet
// listening).
func NewPairing(socketPath string, h *hub.Hub, log *slog.Logger) *PairingServer {
	p := &PairingServer{Server: &Server{socketPath: socketPath, hub: h, log: log}, handshakes: make(chan struct{}, maxHandshakes),
		slots: make(chan struct{}, maxPairingConns), messages: make(chan struct{}, maxMessageConns)}
	p.prepare, p.secure, p.handle = privateDir, ownerOnly, p.serve
	return p
}

// NewSharedPairing is NewPairing for the secured/connector route: the
// socket's directory must be ours with group gid and mode 0750, and the
// socket is given to gid with mode 0660, so connector's relay can connect.
// The group can connect only; every connection must still complete first
// contact and the pairing exchange. It is opt-in: NewPairing stays private.
func NewSharedPairing(socketPath string, gid int, h *hub.Hub, log *slog.Logger) *PairingServer {
	p := NewPairing(socketPath, h, log)
	p.prepare = func(path string) error { return shared.CheckSharedDir(filepath.Dir(path), os.Geteuid(), gid) }
	p.secure = func(path string) error { return shared.SecureSharedSocket(path, gid) }
	return p
}

// privateDir requires the socket's directory to be ours and 0700.
func privateDir(path string) error {
	dir := filepath.Dir(path)
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("pairing socket directory: %w", err)
	}
	if !info.IsDir() || !ownedPrivately(info) {
		return fmt.Errorf("pairing socket directory %s must be ours with mode 0700", dir)
	}
	return nil
}

func ownedPrivately(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() {
		return false
	}
	return info.Mode().Perm()&0o077 == 0
}

func ownerOnly(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("pairing socket mode: %w", err)
	}
	return nil
}

// serve answers one inbound connection: first contact under the handshake
// bound, then pairing, settlement or a message session by its protocol, each
// under its own bound. The network work is bounded and cancelled with the
// server; the store writes are not, so an attempt's outcome always settles.
func (p *PairingServer) serve(ctx context.Context, raw net.Conn) {
	ep, err := p.hub.PairingEndpoint()
	if err != nil {
		raw.Close()
		p.log.Warn("pairing connection refused: no workspace identity")
		return
	}
	netCtx, cancelNet := context.WithTimeout(ctx, hub.AttemptBudget)
	defer cancelNet()
	storeCtx, cancelStore := context.WithTimeout(context.WithoutCancel(ctx), hub.AttemptBudget+hub.PairingGrace)
	defer cancelStore()
	conn, ok := p.handshake(netCtx, raw, ep)
	if !ok {
		return
	}
	defer conn.Close()
	if conn.Protocol() == firstcontact.MessageALPN {
		p.messageSession(ctx, attemptCtx{netCtx, storeCtx}, conn, ep)
		return
	}
	if !take(p.slots) {
		p.log.Warn("pairing connection refused: too many")
		return
	}
	defer release(p.slots)
	p.pairing(netCtx, storeCtx, conn, ep)
}

// handshake runs first contact under the handshake bound.
func (p *PairingServer) handshake(netCtx context.Context, raw net.Conn, ep hub.PairingEndpoint) (*firstcontact.Conn, bool) {
	if !take(p.handshakes) {
		raw.Close()
		p.log.Warn("pairing connection refused: too many handshakes")
		return nil, false
	}
	defer release(p.handshakes)
	opts := firstcontact.ServerOptions{ContactPins: ep.ContactPins, MessagePins: ep.MessagePins}
	conn, err := firstcontact.ServerWith(netCtx, raw, ep.Identity, ep.Clock, opts)
	if err != nil {
		p.log.Info("pairing first contact failed", "cause", hub.PairingCause(err))
		return nil, false
	}
	return conn, true
}

// take claims a slot without waiting; release returns it.
func take(slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func release(slots chan struct{}) { <-slots }

// pairing answers a pairing exchange, or a contact's settlement.
func (p *PairingServer) pairing(netCtx, storeCtx context.Context, conn *firstcontact.Conn, ep hub.PairingEndpoint) {
	if conn.Protocol() == firstcontact.ContactALPN {
		p.settle(storeCtx, conn.Frames(netCtx), conn, ep)
		return
	}
	request, err := ep.Inviter.Serve(storeCtx, conn.Frames(netCtx), conn.Connection())
	if request.IdentityKey != "" {
		ep.Changed() // a new or repeated request, committed whatever followed
	}
	if err != nil {
		p.log.Info("pairing attempt ended", "cause", hub.PairingCause(err))
		return
	}
	p.log.Info("pairing request committed")
}

// attemptCtx bounds one connection's first round: its network work, and
// its store writes, which outlive a cancelled server.
type attemptCtx struct{ net, store context.Context }

// messageSession answers a confirmed contact's first round, one per
// contact at a time, within MessageBudget. If both sides agree, the
// connection then stays open as a live link (#222), which holds no message
// slot and lives with the server, not the first round's budget.
func (p *PairingServer) messageSession(ctx context.Context, attempt attemptCtx, conn *firstcontact.Conn, ep hub.PairingEndpoint) {
	if !take(p.messages) {
		p.log.Warn("message session refused: too many")
		return
	}
	claim, carried := p.firstRound(attempt.net, attempt.store, conn, ep)
	release(p.messages)
	if claim == nil {
		return
	}
	err := claim.Run(ctx, func(ctx context.Context) messaging.Frames { return conn.MessageFrames(ctx) }, carried)
	p.log.Info("message link ended", "cause", hub.PairingCause(err))
}

// firstRound answers the first round, and returns the live link's claim
// when both sides stay connected.
func (p *PairingServer) firstRound(netCtx, storeCtx context.Context, conn *firstcontact.Conn, ep hub.PairingEndpoint) (*hub.LiveClaim, *messaging.Counted) {
	contact := conn.Connection().PeerLeaf
	end, free := ep.BeginMessages(contact)
	if !free {
		p.log.Info("message session refused: one already running for this contact")
		return nil, nil
	}
	sessionCtx, cancel := context.WithTimeout(netCtx, hub.MessageBudget)
	defer cancel()
	counted := &messaging.Counted{Frames: conn.MessageFrames(sessionCtx)}
	s := messaging.Session{Frames: counted, Store: ep.Messages, Contact: contact, Now: ep.Clock}
	result, version, err := s.OpenServe(storeCtx)
	end(result.Moved())
	if err != nil {
		p.log.Info("message session ended", "cause", hub.PairingCause(err))
		return nil, nil
	}
	p.log.Info("message session answered", "received", result.Received, "sent", result.Sent)
	if version < messaging.LiveVersion {
		return nil, nil
	}
	return p.stayOpen(s, contact, ep), counted
}

// stayOpen answers whether the connection stays open as a live link.
func (p *PairingServer) stayOpen(s messaging.Session, contact string, ep hub.PairingEndpoint) *hub.LiveClaim {
	var claim *hub.LiveClaim
	can := false
	if ep.ClaimLink != nil {
		claim, can = ep.ClaimLink(contact)
	}
	on, err := s.AnswerLive(can)
	if err == nil && on {
		return claim
	}
	if can {
		claim.Release()
	}
	return nil
}

// settle answers a paired workspace's settlement on a pinned contact
// connection.
func (p *PairingServer) settle(storeCtx context.Context, frames peer.Frames, conn *firstcontact.Conn, ep hub.PairingEndpoint) {
	// A delivery may be recorded even when a later step fails.
	peerKey := conn.Connection().PeerLeaf
	before := ep.RequestMark(peerKey)
	defer func() {
		if ep.RequestMark(peerKey) != before {
			ep.Changed()
		}
	}()
	if err := ep.Inviter.ServeSettle(storeCtx, frames, conn.Connection()); err != nil {
		p.log.Info("pairing settlement ended", "cause", hub.PairingCause(err))
		return
	}
	p.log.Info("pairing settlement answered")
}
