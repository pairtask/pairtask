package desktop

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// When the always-on agent is installed for a data directory, it owns the
// node: every other copy (the desktop app's helper, Claude's extension
// copies) forwards to it. A copy that starts before the agent waits a
// while for it; one that runs the node because the agent wasn't there
// hands the node over as soon as the agent asks for it.

// RoleAgent is the always-on agent's role.
const RoleAgent = "agent"

// Files in the data directory that say who runs the node.
const (
	agentMarkerName  = "agent-installed" // the agent serves this directory
	agentWaitingName = "agent-waiting"   // the agent wants the node; refreshed while it waits
	ownerInfoName    = "owner.json"      // who runs the node now
)

// agentWait bounds how long a client waits for an installed agent to
// answer before it runs the node itself.
const agentWait = 10 * time.Second

// yieldPoll is how often a client running the node looks for a waiting
// agent.
const yieldPoll = time.Second

// agentWaitingFresh: a waiting mark older than this is from an agent that
// stopped waiting without removing it.
const agentWaitingFresh = 15 * time.Second

// MarkAgentInstalled notes that the agent serves dir.
func MarkAgentInstalled(dir string) error {
	return os.WriteFile(filepath.Join(dir, agentMarkerName), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
}

// UnmarkAgentInstalled removes the note, when the agent is uninstalled.
func UnmarkAgentInstalled(dir string) error {
	err := os.Remove(filepath.Join(dir, agentMarkerName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// AgentInstalled: the agent serves dir.
func AgentInstalled(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, agentMarkerName))
	return err == nil
}

// nodeFiles are the files in a data directory that say who runs its node.
type nodeFiles struct{ dir string }

func (f nodeFiles) path(name string) string { return filepath.Join(f.dir, name) }

// markWaiting refreshes the agent's request for the node.
func (f nodeFiles) markWaiting() error {
	now := time.Now()
	if err := os.Chtimes(f.path(agentWaitingName), now, now); err == nil {
		return nil
	}
	return os.WriteFile(f.path(agentWaitingName), nil, 0o600)
}

// clearWaiting removes the request, once the agent runs the node or stops.
func (f nodeFiles) clearWaiting() {
	_ = os.Remove(f.path(agentWaitingName))
}

// waiting: the agent asked for the node recently.
func (f nodeFiles) waiting() bool {
	info, err := os.Stat(f.path(agentWaitingName))
	return err == nil && time.Since(info.ModTime()) < agentWaitingFresh
}

// prefersAgent: this copy should leave the node to the agent.
func (n *Node) prefersAgent() bool {
	return n.cfg.Role != RoleAgent && AgentInstalled(n.cfg.Dir)
}

// reachAgent forwards to the node's owner as soon as one answers, within
// wait. Called with mu held.
func (n *Node) reachAgent(ctx context.Context, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		f, err := dialOwner(ctx, n.cfg.Dir)
		if err == nil {
			n.fwd = f
			return true
		}
		if errors.Is(err, ErrSocketPathTooLong) || time.Now().After(deadline) {
			return false
		}
		if pause(ctx, 100*time.Millisecond) != nil {
			return false
		}
	}
}

// yieldToAgent watches for the agent while this client runs the node, and
// hands the node over when the agent asks for it. Only the agent asks, so
// a client runs the watch whether or not it knew of an agent at start.
func (n *Node) yieldToAgent() {
	if n.cfg.Role == RoleAgent || !n.yielding.CompareAndSwap(false, true) {
		return
	}
	if !n.spawn(n.watchForAgent) {
		n.yielding.Store(false)
	}
}

// watchForAgent hands the node to the agent when it asks, until this copy
// no longer runs the node or closes.
func (n *Node) watchForAgent() {
	defer n.yielding.Store(false)
	files := nodeFiles{n.cfg.Dir}
	t := time.NewTicker(yieldPoll)
	defer t.Stop()
	for {
		select {
		case <-n.joins.life.Done():
			return
		case <-t.C:
		}
		if files.waiting() && n.yield(n.joins.life) {
			return
		}
	}
}

// yield stops running the node and forwards to the agent that asked for
// it; if the agent doesn't take it in time, this copy runs it again.
// true once this copy no longer owns the node.
func (n *Node) yield(ctx context.Context) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.own == nil {
		return true
	}
	n.cfg.Log.Info("the agent is ready; handing the node to it", "dir", n.cfg.Dir)
	if err := n.own.close(); err != nil {
		n.cfg.Log.Warn("stopping the broker for the agent", "error", err)
	}
	n.own = nil
	if n.reachAgent(ctx, agentWait) {
		return true
	}
	n.cfg.Log.Warn("the agent didn't take the node; this copy runs it again", "dir", n.cfg.Dir)
	if err := n.reach(ctx); err != nil {
		n.cfg.Log.Warn("reaching the node again", "error", err)
	}
	return n.own == nil
}

// ErrNotRunning: no copy runs the node in that data directory now.
var ErrNotRunning = errors.New("no copy runs this node now")

// Connect reaches the copy that runs dir's node, as a client that never
// runs it itself: for status and other read-only callers.
func Connect(ctx context.Context, dir string) (*Node, error) {
	n := newNode(ctx, Config{Dir: dir, Log: slog.New(slog.DiscardHandler)})
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.reachAgent(ctx, 2*time.Second) {
		n.stopJoins()
		return nil, ErrNotRunning
	}
	return n, nil
}
