// Package desktop is a light pairtask node for Claude Desktop: an embedded
// broker that redeems other workspaces' invitations and messages the
// contacts they make, driven in-process by an MCP server on stdio. It
// listens on nothing outside its private data directory: no agent socket,
// no operator socket and no pairing port, so it only ever dials out.
//
// Desktop starts one copy per surface (chat, Cowork, Code), all on the
// same data directory. The copy that takes the database's lock runs the
// broker and shares it on a socket in that directory; the others forward
// their calls to it, and one of them takes over when it exits.
package desktop

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// Config is what a node needs to start.
type Config struct {
	Dir  string // the node's private data directory, created 0700
	Name string // the name it pairs under; "workspace" if empty
	Log  *slog.Logger
	// Dial reaches another workspace; nil dials TCP directly.
	Dial func(ctx context.Context, destination string) (net.Conn, error)
}

// Node is this copy's way to the broker: the broker itself while it owns
// the data directory, else a connection to the copy that does.
type Node struct {
	cfg  Config
	base context.Context // what an owner's broker runs under, never canceled by a call
	mu   sync.Mutex      // guards own and fwd, and every change between them
	own  *owner
	fwd  *forwarder
}

// ErrUnsafeDataDir: the data directory isn't a private directory of ours.
var ErrUnsafeDataDir = errors.New("unsafe data directory")

// ErrNoOwner: another process holds the database but nothing answers on
// its socket in time.
var ErrNoOwner = errors.New("another pairtask desktop node holds the database without sharing it (an older version or a maintenance command): quit Claude Desktop fully and start it again")

// databaseFile is the broker's database in the data directory; the
// workspace identity sits beside it.
const databaseFile = "broker.db"

// takeoverWait bounds how long a copy tries to reach a broker: to take the
// lock, or to reach the copy that holds it.
const takeoverWait = 3 * time.Second

// forwardTries bounds the calls a forwarder retries after reaching a new
// broker, so a broker that keeps failing ends the call.
const forwardTries = 3

// Start takes the node's data directory: as its owner, locking and opening
// the database, recovering interrupted redemptions and sharing the broker;
// or, when another copy owns it, as a forwarder to that copy.
func Start(ctx context.Context, cfg Config) (*Node, error) {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if err := ensureDataDir(cfg.Dir); err != nil {
		return nil, err
	}
	n := &Node{cfg: cfg, base: context.WithoutCancel(ctx)}
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.reach(ctx); err != nil {
		return nil, err
	}
	return n, nil
}

// Owner: this copy runs the broker.
func (n *Node) Owner() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.own != nil
}

// reach makes this copy the owner, or a forwarder to the copy that owns the
// data directory, trying until takeoverWait. Called with mu held.
func (n *Node) reach(ctx context.Context) error {
	deadline := time.Now().Add(takeoverWait)
	for {
		done, err := n.reachOnce(ctx)
		if done || err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return ErrNoOwner
		}
		if err := pause(ctx, 50*time.Millisecond); err != nil {
			return err
		}
	}
}

// reachOnce tries the lock, then the owner's socket, once each.
func (n *Node) reachOnce(ctx context.Context) (bool, error) {
	o, err := startOwner(ctx, n.base, n.cfg)
	if err == nil {
		n.own = o
		n.cfg.Log.Info("this copy runs the broker", "dir", n.cfg.Dir)
		return true, nil
	}
	if !errors.Is(err, store.ErrDatabaseInUse) {
		return false, err
	}
	f, err := dialOwner(ctx, n.cfg.Dir)
	if err != nil {
		return false, nil
	}
	n.fwd = f
	return true, nil
}

// pause waits d, or until ctx ends.
func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Call hands one operator request to the broker and returns its answer, as
// the operator socket would, at this build's protocol version; a request
// without a message ID gets a fresh one, kept if the call is retried.
func (n *Node) Call(ctx context.Context, msg protocol.Message) protocol.Message {
	msg.ProtocolVersion = protocol.Version
	if msg.MessageID == "" {
		msg.MessageID = uuid.NewString()
	}
	for range forwardTries {
		out, done, f := n.attempt(ctx, msg)
		if done {
			return out
		}
		if ctx.Err() != nil {
			return unavailable(ctx.Err())
		}
		if err := n.replace(ctx, f); err != nil {
			return unavailable(err)
		}
	}
	return unavailable(errBroken)
}

// attempt is one try at the broker this copy reaches now: done with its
// answer, or not, with the forwarder that failed (nil when there was none,
// after a takeover that reached no broker).
func (n *Node) attempt(ctx context.Context, msg protocol.Message) (protocol.Message, bool, *forwarder) {
	o, f := n.current()
	if o != nil {
		return o.call(ctx, msg), true, nil
	}
	if f == nil {
		return protocol.Message{}, false, nil
	}
	out, err := f.call(ctx, msg)
	return out, err == nil, f
}

// current is the owner or the forwarder this copy uses now.
func (n *Node) current() (*owner, *forwarder) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.own, n.fwd
}

// replace drops a forwarder whose owner went away, if any, and reaches the
// broker again, unless another call already did.
func (n *Node) replace(ctx context.Context, gone *forwarder) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.own != nil || n.fwd != gone {
		return nil
	}
	if gone != nil {
		gone.close()
	}
	n.fwd = nil
	return n.reach(ctx)
}

// unavailable is a call that reached no broker, as the broker's own errors
// read.
func unavailable(err error) protocol.Message {
	return protocol.Message{Type: protocol.TypeError, Code: "node_unavailable", Detail: err.Error()}
}

// Close stops the broker this copy owns, or drops its connection to the
// owner.
func (n *Node) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fwd != nil {
		n.fwd.close()
		n.fwd = nil
	}
	if n.own == nil {
		return nil
	}
	err := n.own.close()
	n.own = nil
	return err
}
