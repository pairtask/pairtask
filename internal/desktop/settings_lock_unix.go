//go:build darwin || linux

package desktop

import (
	"os"
	"path/filepath"
	"syscall"
)

// lockSettings holds the data directory's settings lock until unlock is
// called: one process at a time reads, merges and writes the settings, so
// two copies started together each keep what they were given.
func lockSettings(dir string) (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(dir, settingsName+".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
