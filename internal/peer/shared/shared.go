// Package shared checks the group-shared directories and sockets that join
// secured and connector for pairing (#178): the broker's inbound pairing
// socket and connector's egress socket. Each is created by its owner in a
// directory only that owner can write, and a single group may connect.
package shared

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// ErrUnsafe is a shared directory or socket that doesn't match the rule.
var ErrUnsafe = errors.New("shared: unsafe path")

// CheckSharedDir requires dir to be a real directory owned by uid, with
// group gid and mode exactly 0750: only its owner writes, the group may
// reach the socket inside, others nothing.
func CheckSharedDir(dir string, uid, gid int) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrUnsafe, dir, err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o750 {
		return fmt.Errorf("%w: %s must be a directory with mode 0750", ErrUnsafe, dir)
	}
	if !ownedBy(info, uid, gid) {
		return fmt.Errorf("%w: %s must be owned by uid %d and gid %d", ErrUnsafe, dir, uid, gid)
	}
	return nil
}

func ownedBy(info fs.FileInfo, uid, gid int) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(st.Uid) == uid && int(st.Gid) == gid
}

// SecureSharedSocket gives a freshly bound socket to the group, mode 0660.
func SecureSharedSocket(path string, gid int) error {
	if err := os.Chown(path, -1, gid); err != nil {
		return fmt.Errorf("shared socket group: %w", err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		return fmt.Errorf("shared socket mode: %w", err)
	}
	return nil
}
