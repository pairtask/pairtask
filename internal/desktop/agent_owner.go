package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
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

// markAgentWaiting refreshes the agent's request for the node.
func markAgentWaiting(dir string) error {
	path := filepath.Join(dir, agentWaitingName)
	now := time.Now()
	if err := os.Chtimes(path, now, now); err == nil {
		return nil
	}
	return os.WriteFile(path, nil, 0o600)
}

// clearAgentWaiting removes the request, once the agent runs the node or
// stops.
func clearAgentWaiting(dir string) {
	_ = os.Remove(filepath.Join(dir, agentWaitingName))
}

// agentWaiting: the agent asked for the node recently.
func agentWaiting(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, agentWaitingName))
	return err == nil && time.Since(info.ModTime()) < agentWaitingFresh
}

// OwnerInfo is who runs the node now, as that copy recorded it.
type OwnerInfo struct {
	PID     int       `json:"pid"`
	Role    string    `json:"role"` // "agent", or "client" for any other copy
	Version string    `json:"version,omitempty"`
	Since   time.Time `json:"since"`
}

// Running: the recorded process is alive.
func (o OwnerInfo) Running() bool {
	if o.PID <= 0 {
		return false
	}
	err := syscall.Kill(o.PID, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// ReadOwnerInfo is the recorded owner of dir's node; ok is false when no
// copy recorded one.
func ReadOwnerInfo(dir string) (OwnerInfo, bool, error) {
	var o OwnerInfo
	data, err := os.ReadFile(filepath.Join(dir, ownerInfoName))
	if errors.Is(err, fs.ErrNotExist) {
		return o, false, nil
	}
	if err != nil {
		return o, false, err
	}
	if err := json.Unmarshal(data, &o); err != nil {
		return o, false, fmt.Errorf("%s can't be read: %w", ownerInfoName, err)
	}
	return o, true, nil
}

// recordOwner notes this process as the node's owner.
func recordOwner(dir string, cfg Config) error {
	role := cfg.Role
	if role == "" {
		role = "client"
	}
	data, err := json.Marshal(OwnerInfo{PID: os.Getpid(), Role: role, Version: cfg.Version, Since: time.Now().UTC()})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ownerInfoName), data, 0o600)
}

// clearOwner removes the note, if it is still this process's.
func clearOwner(dir string) {
	if o, ok, _ := ReadOwnerInfo(dir); ok && o.PID == os.Getpid() {
		_ = os.Remove(filepath.Join(dir, ownerInfoName))
	}
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
	started := n.spawn(func() {
		defer n.yielding.Store(false)
		t := time.NewTicker(yieldPoll)
		defer t.Stop()
		for {
			select {
			case <-n.joins.life.Done():
				return
			case <-t.C:
			}
			if agentWaiting(n.cfg.Dir) && n.yield(n.joins.life) {
				return
			}
		}
	})
	if !started {
		n.yielding.Store(false)
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
