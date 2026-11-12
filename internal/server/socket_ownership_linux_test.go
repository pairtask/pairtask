//go:build linux

package server

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A just-unlinked socket's inode can be reused by a regular replacement.
// Simulate that reuse in the two FileInfo snapshots, without depending on
// the filesystem allocator to make the race happen during this test.
func TestOwnsSocketRefusesRegularFileWithReusedInode(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "broker.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	want, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if !ownsSocket(want, want) {
		t.Fatal("the original socket was not recognized as owned")
	}

	replacementPath := filepath.Join(t.TempDir(), "replacement")
	if err := os.WriteFile(replacementPath, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.Lstat(replacementPath)
	if err != nil {
		t.Fatal(err)
	}
	originalStat := want.Sys().(*syscall.Stat_t)
	replacementStat := got.Sys().(*syscall.Stat_t)
	replacementStat.Dev, replacementStat.Ino = originalStat.Dev, originalStat.Ino
	if !os.SameFile(want, got) {
		t.Fatal("test did not simulate inode reuse")
	}
	if got.Mode()&os.ModeSocket != 0 {
		t.Fatal("replacement unexpectedly has socket mode")
	}
	if ownsSocket(want, got) {
		t.Fatal("regular replacement was mistaken for the owned socket")
	}
}
