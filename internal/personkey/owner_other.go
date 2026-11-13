//go:build !unix

package personkey

import "io/fs"

// ownedByUs can't be told here; the mode checks still apply.
func ownedByUs(fs.FileInfo) bool { return true }
