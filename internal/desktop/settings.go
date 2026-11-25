package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// The node's settings live in its data directory, so they hold whichever
// copy runs the broker: the always-on agent, the desktop app's helper or a
// Claude extension copy. A copy started with a setting stores it; a copy
// started without one uses what is stored. The latest stored value wins.

// settingsName is the settings file in the data directory.
const settingsName = "settings.json"

// Settings is what the person configured for this node.
type Settings struct {
	Name     string `json:"name,omitempty"`     // the name this node pairs under
	Pairbook string `json:"pairbook,omitempty"` // the company's Pairbook, https://HOST
	Address  string `json:"address,omitempty"`  // the Workspace address setting
	Notify   *bool  `json:"notify,omitempty"`   // system notifications; on when unset
}

// notifies: notifications are on unless turned off.
func (s Settings) notifies() bool {
	return s.Notify == nil || *s.Notify
}

// over is s with every setting given sets.
func (s Settings) over(given Settings) Settings {
	if given.Name != "" {
		s.Name = given.Name
	}
	if given.Pairbook != "" {
		s.Pairbook = given.Pairbook
	}
	if given.Address != "" {
		s.Address = given.Address
	}
	if given.Notify != nil {
		s.Notify = given.Notify
	}
	return s
}

// ReadSettings is the node's stored settings in dir: none when there are
// none yet.
func ReadSettings(dir string) (Settings, error) {
	var s Settings
	data, err := os.ReadFile(filepath.Join(dir, settingsName))
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{}, fmt.Errorf("the settings in %s can't be read: %w", settingsName, err)
	}
	return s, nil
}

// errBadPairbook: the Pairbook setting isn't a bare https://HOST address.
var errBadPairbook = errors.New("the Pairbook setting must be the company's Pairbook address alone, like https://pairbook.example.com (no path, join link or command); the previous setting is kept")

// storeSettings keeps the settings this copy was given, over the stored
// ones, and returns what now holds. A Pairbook setting that isn't an
// https://HOST address is not stored: problem says so, and the stored one
// stays. err is a settings file that couldn't be read or written.
func storeSettings(dir string, given Settings) (now Settings, problem, err error) {
	stored, err := ReadSettings(dir)
	if err != nil {
		return given, nil, err
	}
	if given.Pairbook != "" && pairbookOrigin(given.Pairbook) == "" {
		given.Pairbook, problem = "", errBadPairbook
	}
	next := stored.over(given)
	if sameSettings(next, stored) {
		return stored, problem, nil
	}
	return next, problem, writeSettings(dir, next)
}

// sameSettings compares two settings, notify by value.
func sameSettings(a, b Settings) bool {
	return a.Name == b.Name && a.Pairbook == b.Pairbook && a.Address == b.Address && a.notifies() == b.notifies() && (a.Notify == nil) == (b.Notify == nil)
}

// writeSettings replaces the stored settings: a synced temporary file,
// renamed over them, in a synced directory.
func writeSettings(dir string, s Settings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, settingsName+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // nothing left behind when a step fails
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := writeSynced(tmp, data); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, settingsName)); err != nil {
		return err
	}
	return syncDir(dir)
}

// settingsWatch notices when the stored settings change.
type settingsWatch struct {
	dir  string
	seen time.Time
	size int64
}

// changed: the settings file differs from the last time it was looked at.
func (w *settingsWatch) changed() bool {
	info, err := os.Stat(filepath.Join(w.dir, settingsName))
	if err != nil {
		return false
	}
	if info.ModTime().Equal(w.seen) && info.Size() == w.size {
		return false
	}
	w.seen, w.size = info.ModTime(), info.Size()
	return true
}

// settingsProblem is the setting this copy didn't keep, for the person.
func (n *Node) settingsProblem() string {
	if n.problem == nil {
		return ""
	}
	return n.problem.Error()
}
