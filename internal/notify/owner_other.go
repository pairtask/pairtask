//go:build !unix

package notify

import "io/fs"

// FileOwner is unknown here.
func FileOwner(fs.FileInfo) (uid int, known bool) { return 0, false }
