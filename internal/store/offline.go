package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"syscall"
)

// Offline inbox maintenance (#119): an export or archive of a database no
// broker runs on. It opens the file as it is, never through Open, so it
// migrates nothing; and it holds the database's lock for its whole run.

// OfflineDatabase is a database opened for offline maintenance.
type OfflineDatabase struct {
	path string
	lock *DatabaseLock
	db   *sql.DB
}

// OpenOffline locks and opens an existing database, read-only: a
// single-linked regular file, never created here. Only an archive's own
// transaction writes, on a connection of its own.
func OpenOffline(path string) (*OfflineDatabase, error) {
	resolved, err := offlineSource(path)
	if err != nil {
		return nil, err
	}
	lock, err := LockDatabase(resolved)
	if err != nil {
		return nil, err
	}
	db, err := openRaw(resolved, "ro")
	if err != nil {
		lock.Close()
		return nil, err
	}
	return &OfflineDatabase{path: resolved, lock: lock, db: db}, nil
}

// offlineSource resolves the database and refuses what isn't one existing
// file with one name: another hard link would name another lock.
func offlineSource(path string) (string, error) {
	resolved, err := resolveDatabase(path)
	if err != nil {
		return "", fmt.Errorf("the database %q: %w", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("the database %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("the database %q is not a regular file", path)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
		return "", fmt.Errorf("the database %q has %d hard links; maintenance needs its only name", path, st.Nlink)
	}
	return resolved, nil
}

// openRaw opens the file as it is, read-only ("ro") or read-write ("rw")
// and never creating it, on one connection.
func openRaw(path, mode string) (*sql.DB, error) {
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=" + mode + "&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open the database: %w", err)
	}
	return db, nil
}

// writer opens the database for writing, under the lock already held.
func (o *OfflineDatabase) writer() (*sql.DB, error) { return openRaw(o.path, "rw") }

// Close closes the database, then releases its lock.
func (o *OfflineDatabase) Close() error {
	return errors.Join(o.db.Close(), o.lock.Close())
}
