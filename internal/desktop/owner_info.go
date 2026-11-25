package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

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
func (f nodeFiles) recordOwner(cfg Config) error {
	role := cfg.Role
	if role == "" {
		role = "client"
	}
	data, err := json.Marshal(OwnerInfo{PID: os.Getpid(), Role: role, Version: cfg.Version, Since: time.Now().UTC()})
	if err != nil {
		return err
	}
	return os.WriteFile(f.path(ownerInfoName), data, 0o600)
}

// clearOwner removes the note, if it is still this process's.
func (f nodeFiles) clearOwner() {
	if o, ok, _ := ReadOwnerInfo(f.dir); ok && o.PID == os.Getpid() {
		_ = os.Remove(f.path(ownerInfoName))
	}
}
