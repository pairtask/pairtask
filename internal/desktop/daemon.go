package desktop

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/pairtask/pairtask/internal/store"
)

// The always-on agent: one long-lived process that owns the data directory,
// so the heartbeat, relay slots, message sync and notifications run whether
// or not a chat client is open. Every client (Claude's extension copies,
// the desktop app, other MCP clients) then forwards to it, as copies
// already do to whichever one owns the directory.

// RunDaemon becomes the data directory's owner, waiting while another copy
// holds it (trying again every retry), and runs the broker until ctx ends.
// It never forwards: a daemon that doesn't own does nothing visible, so no
// prompt or notification repeats while it waits.
func RunDaemon(ctx context.Context, cfg Config, retry time.Duration) error {
	n, err := own(ctx, cfg, retry)
	if err != nil {
		if ctx.Err() != nil {
			return nil // stopped while waiting
		}
		return err
	}
	n.cfg.Log.Info("the agent runs the broker", "dir", n.cfg.Dir)
	<-ctx.Done()
	return n.Close()
}

// own takes the data directory's lock as soon as it is free.
func own(ctx context.Context, cfg Config, retry time.Duration) (*Node, error) {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if err := ensureDataDir(cfg.Dir); err != nil {
		return nil, err
	}
	// An agent that can't share its node would hold the directory while no
	// client could reach it: refuse before taking the lock.
	if _, err := socketPath(cfg.Dir); err != nil {
		return nil, err
	}
	waiting := false
	for {
		n := newNode(ctx, cfg)
		o, err := startOwner(ctx, n.base, cfg)
		if err == nil {
			n.mu.Lock()
			n.own = o
			n.startPairbook()
			n.mu.Unlock()
			return n, nil
		}
		n.stopJoins()
		if !errors.Is(err, store.ErrDatabaseInUse) {
			return nil, err
		}
		if !waiting {
			cfg.Log.Info("another copy runs the broker; the agent takes over when it exits", "dir", cfg.Dir)
			waiting = true
		}
		if err := pause(ctx, retry); err != nil {
			return nil, err
		}
	}
}
