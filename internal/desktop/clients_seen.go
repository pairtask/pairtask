package desktop

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Each MCP client that starts this node's server says its name when it
// connects (its clientInfo). The node keeps when each was last seen, so
// the app can tell a client that is merely configured from one that has
// actually started.

const clientsSeenName = "clients-seen.json"

// ReadSeenClients is when each client name last connected.
func ReadSeenClients(dir string) (map[string]time.Time, error) {
	seen := map[string]time.Time{}
	data, err := os.ReadFile(filepath.Join(dir, clientsSeenName))
	if errors.Is(err, fs.ErrNotExist) {
		return seen, nil
	}
	if err != nil {
		return seen, err
	}
	return seen, json.Unmarshal(data, &seen)
}

// noteClient records that the named client connected just now; failing to
// record it only costs the app a status line.
func (n *Node) noteClient(name string) {
	if name == "" || len(name) > 100 {
		return
	}
	unlock, err := lockSettings(n.cfg.Dir)
	if err != nil {
		return
	}
	defer unlock()
	seen, _ := ReadSeenClients(n.cfg.Dir)
	seen[name] = time.Now().UTC()
	data, err := json.Marshal(seen)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(n.cfg.Dir, clientsSeenName+".*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if writeSynced(tmp, data) == nil {
		_ = os.Rename(tmp.Name(), filepath.Join(n.cfg.Dir, clientsSeenName))
	}
}
