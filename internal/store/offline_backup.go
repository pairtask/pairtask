package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
)

// Backup is a published snapshot of the whole database: every table, the
// committed WAL included. It holds the SQLite file only, never the <db>.peer
// directory beside it.
type Backup struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// ErrBackupExists: the destination names something already, which a backup
// never replaces.
var ErrBackupExists = errors.New("the backup destination already exists; nothing replaced")

// Export publishes a backup at dest and changes nothing in the database.
func (o *OfflineDatabase) Export(ctx context.Context, dest string) (Backup, error) {
	target, err := newBackupTarget(dest)
	if err != nil {
		return Backup{}, err
	}
	if err := target.create(); err != nil {
		return Backup{}, err
	}
	defer target.cleanup()
	if err := o.writeSnapshot(ctx, target); err != nil {
		return Backup{}, err
	}
	if err := target.publish(); err != nil {
		return Backup{}, err
	}
	return target.describe()
}

// backupTarget is a backup on its way: the private file it is written to,
// then the name it is published under.
type backupTarget struct{ dest, tmp string }

// newBackupTarget refuses a destination that names anything already.
func newBackupTarget(dest string) (backupTarget, error) {
	dest, err := filepath.Abs(dest)
	if err != nil {
		return backupTarget{}, err
	}
	_, err = os.Lstat(dest)
	if err == nil {
		return backupTarget{}, ErrBackupExists
	}
	if !errors.Is(err, os.ErrNotExist) {
		return backupTarget{}, err
	}
	return backupTarget{dest: dest}, nil
}

// create makes the private, empty file beside the destination.
func (b *backupTarget) create() error {
	file, err := os.CreateTemp(filepath.Dir(b.dest), ".pairtask-backup-*")
	if err != nil {
		return fmt.Errorf("create the backup: %w", err)
	}
	b.tmp = file.Name()
	if err := errors.Join(file.Chmod(0600), file.Close()); err != nil {
		b.cleanup()
		return err
	}
	return nil
}

// cleanup removes the private file; a published backup keeps its own name.
func (b backupTarget) cleanup() { os.Remove(b.tmp) }

func (o *OfflineDatabase) writeSnapshot(ctx context.Context, b backupTarget) error {
	// VACUUM INTO writes a consistent copy into the empty file, the
	// database itself unchanged.
	if _, err := o.db.ExecContext(ctx, `VACUUM INTO ?`, b.tmp); err != nil {
		return fmt.Errorf("write the backup: %w", err)
	}
	if err := b.check(ctx); err != nil {
		return err
	}
	return b.sync()
}

// check reads the copy back as a database.
func (b backupTarget) check(ctx context.Context) error {
	uri := url.URL{Scheme: "file", Path: b.tmp, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&result); err != nil {
		return fmt.Errorf("check the backup: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("the backup failed its check: %s", result)
	}
	return nil
}

func (b backupTarget) sync() error {
	file, err := os.OpenFile(b.tmp, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

// publish links the finished copy at the destination, never replacing
// anything there, and syncs the directory so the name survives a crash.
func (b backupTarget) publish() error {
	if err := os.Link(b.tmp, b.dest); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrBackupExists
		}
		return fmt.Errorf("publish the backup: %w", err)
	}
	dir, err := os.Open(filepath.Dir(b.dest))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func (b backupTarget) describe() (Backup, error) {
	file, err := os.Open(b.dest)
	if err != nil {
		return Backup{}, err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, file)
	if err != nil {
		return Backup{}, err
	}
	return Backup{Path: b.dest, SHA256: hex.EncodeToString(hash.Sum(nil)), Bytes: n}, nil
}
