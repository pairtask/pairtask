//go:build !darwin && !linux

package desktop

import "sync"

// settingsMu serializes settings updates within this process; elsewhere
// than macOS and Linux that is all that is enforced.
var settingsMu sync.Mutex

func lockSettings(string) (func(), error) {
	settingsMu.Lock()
	return settingsMu.Unlock, nil
}
