//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/egress"
)

func routeCLIPath(t *testing.T) (string, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "peer-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0750); err != nil {
		t.Fatal(err)
	}
	group, err := user.LookupGroupId(strconv.Itoa(os.Getegid()))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "egress.sock"), group.Name
}

func TestPeerRouteCLIAcceptanceRunsEgressWithGroupModeAndStops(t *testing.T) {
	path, group := routeCLIPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"peer", "egress", "--socket", path, "--group", group}, &stdout, &stderr)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("egress exit=%d: %s", code, stderr.String())
			}
		case <-time.After(2 * time.Second):
			t.Error("egress CLI did not stop")
		}
	})
	awaitRouteCLISocket(t, path)
	callCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	c, err := egress.Dial(callCtx, path, "127.0.0.1:443")
	if c != nil {
		c.Close()
		t.Fatal("CLI allowed forbidden destination")
	}
	if !errors.Is(err, egress.ErrRefused) {
		t.Fatalf("loopback refusal: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0660 || int(info.Sys().(*syscall.Stat_t).Gid) != os.Getegid() {
		t.Fatal("CLI socket did not get configured group permissions")
	}
}

func awaitRouteCLISocket(t *testing.T, path string) {
	t.Helper()
	until := time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("peer egress CLI never created its socket")
}

func TestPeerRouteCLIAcceptanceExistingPathNeverReplaced(t *testing.T) {
	path, group := routeCLIPath(t)
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"peer", "egress", "--socket", path, "--group", group}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("existing endpoint accepted")
	}
	after, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("existing inode replaced")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep" {
		t.Fatal("existing content changed")
	}
}

func TestPeerRouteCLIAcceptanceMissingConfigurationRefused(t *testing.T) {
	for _, args := range [][]string{{"peer"}, {"peer", "unknown"}, {"peer", "relay"}, {"peer", "egress"}} {
		var stdout, stderr bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		code := run(ctx, args, &stdout, &stderr)
		cancel()
		if code == 0 {
			t.Fatalf("missing configuration accepted: %v", args)
		}
	}
}
