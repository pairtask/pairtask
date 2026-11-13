//go:build unix

package personkey

import (
	"io/fs"
	"os"
	"syscall"
)

// ownedByUs: info's owner is this process's effective user.
func ownedByUs(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}
