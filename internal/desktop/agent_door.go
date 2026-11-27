package desktop

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pairtask/pairtask/internal/server"
)

// AgentSocketFile is where the owning copy serves this Mac's agents, in
// the data directory: each app connected with its own agent ID registers
// there as a Box agent registers with its broker, with its own inbox,
// rooms, mentions and replies.
const AgentSocketFile = "agents.sock"

// AgentSocketPath is where dir's node serves its agents.
func AgentSocketPath(dir string) string { return filepath.Join(dir, AgentSocketFile) }

// openAgentDoor serves this node's agents on the data directory's agent
// socket until the owner stops, replacing one a former owner left: the
// lock says none is running. A path too long to bind leaves the node
// without agents, as it was before them.
func (o *owner) openAgentDoor(dir string) error {
	path := AgentSocketPath(dir)
	if len(path) > maxSocketPath() {
		o.log.Warn("this node can't serve agents", "error", ErrSocketPathTooLong, "socket", path)
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	o.agents = server.NewPrivate(path, o.hub, o.log)
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		if err := o.agents.Listen(o.ctx); err != nil {
			o.log.Warn("this node stopped serving agents", "error", err, "socket", path)
		}
	}()
	return nil
}

// closeAgentDoor stops serving agents and removes the socket.
func (o *owner) closeAgentDoor() {
	if o.agents != nil {
		_ = o.agents.Close()
	}
}
