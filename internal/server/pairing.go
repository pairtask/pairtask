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
	"github.com/pairtask/pairtask/internal/peer/shared"
)

// maxPairingConns bounds live inbound pairing connections; more are closed
// at once. The invitation itself admits one attempt at a time.
const maxPairingConns = 4

// PairingServer accepts first-contact pairing connections (#178) on a Unix
// socket, typically forwarded by a relay. Its directory must be private to
// this user; the socket is 0600. Like the agent and operator sockets, an
// existing path is never removed: an operator deletes a stale one.
type PairingServer struct {
	*Server
	slots chan struct{}
}

// NewPairing creates a pairing server bound to socketPath (not yet
// listening).
func NewPairing(socketPath string, h *hub.Hub, log *slog.Logger) *PairingServer {
	p := &PairingServer{Server: &Server{socketPath: socketPath, hub: h, log: log}, slots: make(chan struct{}, maxPairingConns)}
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

// serve answers one pairing connection: first contact, then the inviter.
// The network work is bounded and cancelled with the server; the store
// writes are not, so an attempt's outcome always settles.
func (p *PairingServer) serve(ctx context.Context, raw net.Conn) {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		raw.Close()
		p.log.Warn("pairing connection refused: too many")
		return
	}
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
	conn, err := firstcontact.ServerWith(netCtx, raw, ep.Identity, ep.Clock, firstcontact.ServerOptions{ContactPins: ep.ContactPins})
	if err != nil {
		p.log.Info("pairing first contact failed", "cause", hub.PairingCause(err))
		return
	}
	defer conn.Close()
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
