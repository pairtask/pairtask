//go:build darwin || linux

package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// followLock is held by the one process following a space's enrollment:
// an exclusive, non-blocking lock on a file beside the kept enrollment,
// released when the process ends however it ends.
type followLock struct{ f *os.File }

// takeFollow takes path's lock, making its private folder first. It is
// errFollowedElsewhere when another process holds it; any other error is
// the file system's.
func takeFollow(path string) (*followLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return &followLock{f: f}, nil
	}
	f.Close()
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return nil, errFollowedElsewhere
	}
	return nil, err
}

func (l *followLock) release() {
	if l != nil {
		_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
		l.f.Close()
	}
}
