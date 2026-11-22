//go:build darwin || linux

package desktop

import (
	"os"
	"syscall"
)

// followLock is held by the one process following a space's enrollment:
// an exclusive, non-blocking lock on a file beside the kept enrollment,
// released when the process ends however it ends.
type followLock struct{ f *os.File }

// takeFollow takes path's lock, or reports another process holds it.
func takeFollow(path string) (*followLock, bool) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil, false
	}
	return &followLock{f: f}, true
}

func (l *followLock) release() {
	if l != nil {
		_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
		l.f.Close()
	}
}
