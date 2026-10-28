//go:build linux

package shared_test

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/shared"
)

func TestSharedRouteAcceptanceExactDirectoryOwnerGroupAndMode(t *testing.T) {
	dir := t.TempDir()
	for _, mode := range []os.FileMode{0700, 0750, 0770, 0755, 0777} {
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		err := shared.CheckSharedDir(dir, os.Geteuid(), os.Getegid())
		if (err == nil) != (mode == 0750) {
			t.Fatalf("mode %o: %v", mode, err)
		}
	}
	if err := os.Chmod(dir, 0750); err != nil {
		t.Fatal(err)
	}
	if shared.CheckSharedDir(dir, os.Geteuid()+1, os.Getegid()) == nil {
		t.Fatal("wrong owner accepted")
	}
	if shared.CheckSharedDir(dir, os.Geteuid(), os.Getegid()+1) == nil {
		t.Fatal("wrong group accepted")
	}
}

func TestSharedRouteAcceptanceSymlinkAndFileRefused(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "directory")
	if err := os.Mkdir(dir, 0750); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0750); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{alias, file, filepath.Join(root, "missing")} {
		if shared.CheckSharedDir(path, os.Geteuid(), os.Getegid()) == nil {
			t.Fatalf("unsafe shared directory accepted: %s", path)
		}
	}
}

func TestSharedRouteAcceptanceSocketIsGroupConnectableOnly(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "shared-socket-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "peer.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := shared.SecureSharedSocket(path, os.Getegid()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0660 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	st := info.Sys().(*syscall.Stat_t)
	if int(st.Uid) != os.Geteuid() || int(st.Gid) != os.Getegid() {
		t.Fatalf("unexpected owner/group: %d/%d", st.Uid, st.Gid)
	}
}
