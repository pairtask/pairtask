package desktop

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// ensureDataDir creates dir 0700 if missing, whatever the umask, and then
// accepts only a real directory of ours that no one else can use: the
// database and the identity live in it, so a symlink, another owner or a
// wider mode is refused before either is opened.
func ensureDataDir(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return fmt.Errorf("data directory: %w", err)
	}
	switch err := os.Mkdir(dir, 0o700); {
	case err == nil:
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("data directory: %w", err)
		}
	case !errors.Is(err, fs.ErrExist):
		return fmt.Errorf("data directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("data directory: %w", err)
	}
	if !privateDir(info) {
		return fmt.Errorf("%w: %s must be a directory of ours with mode 0700, not a symlink", ErrUnsafeDataDir, dir)
	}
	return nil
}

// privateDir reports a directory, not a symlink, owned by us with mode 0700.
func privateDir(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.IsDir() && int(st.Uid) == os.Geteuid() && info.Mode().Perm() == 0o700
}
