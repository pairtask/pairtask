//go:build linux

package server

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPairingSocketAcceptancePrivateAndExistingPathsPreserved(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	info, err := os.Lstat(f.pairSocket)
	operatorNoError(t, err)
	operatorEqual(t, "private peer socket", info.Mode().Perm(), os.FileMode(0600))
	parent, err := os.Stat(filepath.Dir(f.pairSocket))
	operatorNoError(t, err)
	operatorEqual(t, "private peer directory", parent.Mode().Perm(), os.FileMode(0700))
	for _, kind := range []string{"file", "symlink", "live", "stale"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(filepath.Dir(f.pairSocket), kind+".sock")
			createPairingExistingPath(t, path, kind)
			before, err := os.Lstat(path)
			operatorNoError(t, err)
			p := NewPairing(path, f.h, testLog())
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := p.Listen(ctx); err == nil {
				t.Fatal("existing path accepted")
			}
			p.Close()
			after, err := os.Lstat(path)
			operatorNoError(t, err)
			operatorEqual(t, "existing inode preserved", os.SameFile(before, after), true)
		})
	}
}
func createPairingExistingPath(t *testing.T, path, kind string) {
	t.Helper()
	switch kind {
	case "file":
		operatorNoError(t, os.WriteFile(path, []byte("keep"), 0600))
	case "symlink":
		operatorNoError(t, os.Symlink("missing", path))
	default:
		ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		operatorNoError(t, err)
		ln.SetUnlinkOnClose(false)
		t.Cleanup(func() { ln.Close() })
		if kind == "stale" {
			operatorNoError(t, ln.Close())
		}
	}
}

func TestPairingSocketAcceptanceUnsafeDirectoryRefused(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	dir, err := os.MkdirTemp("/tmp", "pairing-unsafe-")
	operatorNoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	operatorNoError(t, os.Chmod(dir, 0755))
	path := filepath.Join(dir, "peer.sock")
	p := NewPairing(path, f.h, testLog())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Listen(ctx); err == nil {
		t.Fatal("public directory accepted")
	}
	p.Close()
	_, err = os.Lstat(path)
	operatorEqual(t, "unsafe listener never bound", os.IsNotExist(err), true)
}
