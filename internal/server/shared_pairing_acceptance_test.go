//go:build linux

package server

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func sharedPairingFixture(t *testing.T, f *redeemOperatorFixture) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "shared-pairing-")
	operatorNoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	operatorNoError(t, os.Chmod(dir, 0750))
	path := filepath.Join(dir, "pair.sock")
	p := NewSharedPairing(path, os.Getegid(), f.h, testLog())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Listen(ctx) }()
	t.Cleanup(func() {
		cancel()
		p.Close()
		select {
		case err := <-done:
			operatorNoError(t, err)
		case <-time.After(2 * time.Second):
			t.Error("shared listener did not stop")
		}
	})
	waitPairingSocket(t, path)
	return path
}

func TestSharedPairingAcceptanceOptInModeAndExistingPaths(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	path := sharedPairingFixture(t, f)
	info, err := os.Lstat(path)
	operatorNoError(t, err)
	operatorEqual(t, "group socket mode", info.Mode().Perm(), os.FileMode(0660))
	operatorEqual(t, "configured group", int(info.Sys().(*syscall.Stat_t).Gid), os.Getegid())
	for _, kind := range []string{"file", "symlink", "live", "stale"} {
		t.Run(kind, func(t *testing.T) {
			existing := filepath.Join(filepath.Dir(path), kind+".sock")
			createPairingExistingPath(t, existing, kind)
			before, err := os.Lstat(existing)
			operatorNoError(t, err)
			p := NewSharedPairing(existing, os.Getegid(), f.h, testLog())
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := p.Listen(ctx); err == nil {
				t.Fatal("existing path accepted")
			}
			p.Close()
			after, err := os.Lstat(existing)
			operatorNoError(t, err)
			operatorEqual(t, "existing inode preserved", os.SameFile(before, after), true)
		})
	}
}

func TestSharedPairingAcceptanceUnsafeDirectoriesNeverBind(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	for _, mode := range []os.FileMode{0700, 0755, 0770} {
		dir, err := os.MkdirTemp("/tmp", "unsafe-shared-")
		operatorNoError(t, err)
		t.Cleanup(func() { os.RemoveAll(dir) })
		operatorNoError(t, os.Chmod(dir, mode))
		path := filepath.Join(dir, "peer.sock")
		p := NewSharedPairing(path, os.Getegid(), f.h, testLog())
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if err := p.Listen(ctx); err == nil {
			t.Fatal("unsafe shared directory accepted")
		}
		cancel()
		p.Close()
		_, err = os.Lstat(path)
		operatorEqual(t, "unsafe endpoint not created", os.IsNotExist(err), true)
	}
}
