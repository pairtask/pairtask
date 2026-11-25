package desktop_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/desktop"
)

// startsAs is whether a fresh copy on dir becomes the owner; it closes it.
func startsAs(t *testing.T, dir string) bool {
	t.Helper()
	n, err := desktop.Start(context.Background(), desktop.Config{Dir: dir, Name: "probe"})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	return n.Owner()
}

// eventually waits for cond, failing after a few seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The agent waits while a client's copy owns the directory, takes over when
// that copy exits, makes later copies forwarders, and on stop releases the
// directory for the next owner.
func TestDaemonTakesOverAndReleases(t *testing.T) {
	dir := sharedNodeDir(t)
	client := startSharedNode(t, dir)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- desktop.RunDaemon(ctx, desktop.Config{Dir: dir, Name: "agent"}, 20*time.Millisecond) }()

	time.Sleep(100 * time.Millisecond)
	if !client.n.Owner() {
		t.Fatal("the waiting agent took the directory from the client's copy")
	}
	if err := client.close(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the agent never took over after the client's copy exited", func() bool { return !startsAs(t, dir) })

	stop()
	if err := <-done; err != nil {
		t.Fatalf("agent stopped with %v", err)
	}
	if !startsAs(t, dir) {
		t.Fatal("the stopped agent kept the directory")
	}
}

// An agent stopped while it waits returns cleanly and never owned anything.
func TestDaemonStoppedWhileWaiting(t *testing.T) {
	dir := sharedNodeDir(t)
	client := startSharedNode(t, dir)
	ctx, stop := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer stop()
	if err := desktop.RunDaemon(ctx, desktop.Config{Dir: dir, Name: "agent"}, 20*time.Millisecond); err != nil {
		t.Fatalf("agent stopped while waiting with %v", err)
	}
	if !client.n.Owner() {
		t.Fatal("the client's copy lost the directory")
	}
}

// An agent whose data directory is too long to share refuses at once, so
// it never holds a directory no client can reach.
func TestDaemonRefusesAnUnshareableDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, strings.Repeat("d", 120))
	err := desktop.RunDaemon(context.Background(), desktop.Config{Dir: dir, Name: "agent"}, 20*time.Millisecond)
	if !errors.Is(err, desktop.ErrSocketPathTooLong) {
		t.Fatalf("got %v, want ErrSocketPathTooLong", err)
	}
}
