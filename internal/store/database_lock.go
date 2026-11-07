package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrDatabaseInUse: another broker or an offline maintenance command owns
// the database (#119).
var ErrDatabaseInUse = errors.New("the database is in use: another broker or an offline maintenance command holds its lock")

// DatabaseLock is the ownership of one database file: a broker holds it for
// its lifetime, an offline export or archive for its run. It is an
// exclusive lock on a sidecar next to the database's resolved path, so a
// symlink alias names the same lock. The sidecar is never removed.
type DatabaseLock struct{ file *os.File }

// LockDatabase takes the database's lock without waiting; a lock held
// elsewhere, or one this filesystem can't take, is refused.
func LockDatabase(path string) (*DatabaseLock, error) {
	resolved, err := resolveDatabase(path)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(resolved+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open the database lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, lockError(err)
	}
	return &DatabaseLock{file: file}, nil
}

func lockError(err error) error {
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrDatabaseInUse
	}
	return fmt.Errorf("the database lock can't be taken here, so nothing runs unlocked: %w", err)
}

// Close releases the lock.
func (l *DatabaseLock) Close() error { return l.file.Close() }

// resolveDatabase is the database's path with every symlink resolved; a
// database not created yet resolves through its directory.
func resolveDatabase(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.Base(path)), nil
}
