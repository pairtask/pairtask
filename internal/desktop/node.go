// Package desktop is a light pairtask node for Claude Desktop: an embedded
// broker that redeems other workspaces' invitations and messages the
// contacts they make, driven in-process by an MCP server on stdio. It
// listens on nothing: no agent socket, no operator socket and no pairing
// port, so it only ever dials out.
package desktop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/peer/pake"
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

// Node is a running embedded broker that owns its data directory.
type Node struct {
	hub    *hub.Hub
	st     *store.Store
	lock   *store.DatabaseLock
	cancel context.CancelFunc
}

// databaseFile is the broker's database in the data directory; the
// workspace identity sits beside it.
const databaseFile = "broker.db"

// Start locks and opens the node's database, recovers interrupted
// redemptions and runs its hub until Close.
func Start(ctx context.Context, cfg Config) (*Node, error) {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("data directory: %w", err)
	}
	db := filepath.Join(cfg.Dir, databaseFile)
	lock, err := store.LockDatabase(db)
	if err != nil {
		return nil, fmt.Errorf("lock %s (is another pairtask desktop node running?): %w", db, err)
	}
	st, err := store.Open(db)
	if err != nil {
		lock.Close()
		return nil, err
	}
	n := &Node{st: st, lock: lock}
	if err := n.run(ctx, cfg); err != nil {
		return nil, errors.Join(err, st.Close(), lock.Close())
	}
	return n, nil
}

// run builds the hub over the open store and starts it.
func (n *Node) run(ctx context.Context, cfg Config) error {
	lastSeq, err := n.st.LastSeq(ctx)
	if err != nil {
		return err
	}
	h := hub.New(n.st, lastSeq, cfg.Log)
	h.SetPairing(hub.PairingConfig{Dial: dialer(cfg.Dial), Exchanger: pake.New(), Name: cfg.Name})
	if _, err := h.RecoverJoins(ctx); err != nil {
		return fmt.Errorf("recover pairing redemptions: %w", err)
	}
	ctx, n.cancel = context.WithCancel(context.WithoutCancel(ctx))
	n.hub = h
	go h.Run(ctx)
	return nil
}

// dialer is dial, or a direct TCP dial without one.
func dialer(dial func(ctx context.Context, destination string) (net.Conn, error)) func(ctx context.Context, destination string) (net.Conn, error) {
	if dial != nil {
		return dial
	}
	return func(ctx context.Context, destination string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", destination)
	}
}

// Call hands one operator request to the hub and returns its answer, as
// the operator socket would, at this build's protocol version; a request
// without a message ID gets a fresh one.
func (n *Node) Call(ctx context.Context, msg protocol.Message) protocol.Message {
	msg.ProtocolVersion = protocol.Version
	if msg.MessageID == "" {
		msg.MessageID = uuid.NewString()
	}
	return n.hub.SubmitOperator(ctx, os.Getuid(), msg)
}

// Close stops the hub and releases the database.
func (n *Node) Close() error {
	n.cancel()
	<-n.hub.Done()
	return errors.Join(n.st.Close(), n.lock.Close())
}
