//go:build unix

package classifier

import (
	"io/fs"
	"syscall"
)

// FileOwner reports the uid that owns the file, from its platform stat.
func FileOwner(info fs.FileInfo) (uid int, known bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
