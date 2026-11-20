package chat

import (
	"fmt"
	"strconv"

	"github.com/pairtask/pairtask/internal/protocol"
)

// File references on a paired message (#419): one line each, as data. The
// label is the sender's and is never opened; the size and the start of the
// digest are what a reader compares with a file obtained another way.

// digestShown is how much of a SHA-256 a conversation row shows.
const digestShown = 12

// fileRefRows are a message's file references, one line each.
func fileRefRows(files []protocol.FileRef) []string {
	rows := make([]string, 0, len(files))
	for _, f := range files {
		rows = append(rows, "▫ "+safe(f.Path)+" · "+sizeWords(f.Size)+" · sha256 "+safe(shortDigest(f.SHA256))+"…")
	}
	return rows
}

// shortDigest is the start of a digest, or all of a shorter one.
func shortDigest(digest string) string { return digest[:min(len(digest), digestShown)] }

// sizeUnits are the binary units a size is shown in, largest last.
var sizeUnits = []string{"KiB", "MiB", "GiB", "TiB", "PiB"}

// sizeWords is a size for reading, with the exact bytes beside a rounded
// one: "512 bytes", "1.0 MiB (1048576 bytes)".
func sizeWords(size int64) string {
	exact := strconv.FormatInt(size, 10) + " bytes"
	if size < 1024 {
		return exact
	}
	scaled, unit := float64(size), ""
	for _, u := range sizeUnits {
		if scaled < 1024 {
			break
		}
		scaled, unit = scaled/1024, u
	}
	return fmt.Sprintf("%.1f %s (%s)", scaled, unit, exact)
}
