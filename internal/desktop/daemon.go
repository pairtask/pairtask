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
	cfg, err := daemonConfig(cfg)
	if err != nil {
		return nil, err
	}
	for waited := false; ; waited = true {
		n, err := tryOwn(ctx, cfg)
		if !errors.Is(err, store.ErrDatabaseInUse) {
			return n, err
		}
		if !waited {
			cfg.Log.Info("another copy runs the broker; the agent takes over when it exits", "dir", cfg.Dir)
		}
		if err := pause(ctx, retry); err != nil {
			return nil, err
		}
	}
}

// daemonConfig is cfg ready for the agent: a logger, and a data directory
// the agent can share. An agent that couldn't share its node would hold
// the directory while no client could reach it, so that is refused before
// any lock is taken.
func daemonConfig(cfg Config) (Config, error) {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if err := ensureDataDir(cfg.Dir); err != nil {
		return cfg, err
	}
	_, err := socketPath(cfg.Dir)
	return cfg, err
}

// tryOwn takes the lock once: a running node, or the reason it couldn't.
func tryOwn(ctx context.Context, cfg Config) (*Node, error) {
	n := newNode(ctx, cfg)
	o, err := startOwner(ctx, n.base, cfg)
	if err != nil {
		n.stopJoins()
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.own = o
	n.startPairbook()
	return n, nil
}
