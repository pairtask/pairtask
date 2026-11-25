package desktop_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/desktop"
	"github.com/pairtask/pairtask/internal/protocol"
)

// shortDir is a data directory whose socket path fits (macOS limits it).
func shortDir(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "pt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return filepath.Join(root, "node")
}

func startClient(t *testing.T, cfg desktop.Config) *desktop.Node {
	t.Helper()
	n, err := desktop.Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

// startAgent runs the always-on agent until the test ends.
func startAgent(t *testing.T, cfg desktop.Config) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- desktop.RunDaemon(ctx, cfg, 50*time.Millisecond) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("agent: %v", err)
		}
	})
}

func selfName(t *testing.T, n *desktop.Node) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := n.Call(ctx, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpList})
	if out.PairingResult == nil {
		t.Fatalf("pairing list returned %+v", out)
	}
	return out.PairingResult.SelfName
}

// waitUntil waits for ok, failing with what after 10s.
func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func ownerRole(t *testing.T, dir string) string {
	t.Helper()
	o, ok, err := desktop.ReadOwnerInfo(dir)
	if err != nil || !ok {
		return ""
	}
	return o.Role
}

func TestClientHandsTheNodeToTheAgent(t *testing.T) {
	dir := shortDir(t)
	client := startClient(t, desktop.Config{Dir: dir, Name: "home-mac"})
	if !client.Owner() {
		t.Fatal("the first copy didn't run the node")
	}
	if err := desktop.MarkAgentInstalled(dir); err != nil {
		t.Fatal(err)
	}
	// The marker was written after the client started, as on an upgrade; a
	// client started after it yields too.
	late := startClient(t, desktop.Config{Dir: dir})
	startAgent(t, desktop.Config{Dir: dir})
	waitUntil(t, "the agent never got the node", func() bool { return ownerRole(t, dir) == desktop.RoleAgent })
	if late.Owner() {
		t.Fatal("a client runs the node next to the agent")
	}
	if got := selfName(t, late); got != "home-mac" {
		t.Fatalf("the agent pairs as %q, want the stored name home-mac", got)
	}
}

func TestClientStartedAfterTheAgentForwards(t *testing.T) {
	dir := shortDir(t)
	startAgent(t, desktop.Config{Dir: dir, Name: "agent-name"})
	waitUntil(t, "the agent never ran the node", func() bool { return ownerRole(t, dir) == desktop.RoleAgent })
	client := startClient(t, desktop.Config{Dir: dir})
	if client.Owner() {
		t.Fatal("a client took the node from the agent")
	}
	if got := selfName(t, client); got != "agent-name" {
		t.Fatalf("name %q, want agent-name", got)
	}
}

func TestANameGivenToAClientReachesTheRunningNode(t *testing.T) {
	dir := shortDir(t)
	startAgent(t, desktop.Config{Dir: dir})
	waitUntil(t, "the agent never ran the node", func() bool { return ownerRole(t, dir) == desktop.RoleAgent })
	plain := startClient(t, desktop.Config{Dir: dir})
	if got := selfName(t, plain); got != "workspace" {
		t.Fatalf("an unnamed node pairs as %q", got)
	}
	startClient(t, desktop.Config{Dir: dir, Name: "siamion-home"})
	waitUntil(t, "the running node kept its old name", func() bool { return selfName(t, plain) == "siamion-home" })
	// A copy started without a name keeps the stored one.
	startClient(t, desktop.Config{Dir: dir})
	if got := selfName(t, plain); got != "siamion-home" {
		t.Fatalf("an unnamed copy reset the name to %q", got)
	}
}

func TestAPairbookCommandIsNotKeptAsTheSetting(t *testing.T) {
	dir := shortDir(t)
	startClient(t, desktop.Config{Dir: dir, Pairbook: "https://pairbook.example.com"}).Close()
	n := startClient(t, desktop.Config{Dir: dir, Pairbook: "pairtask space join 'https://pairbook.example.com/join#space=x'"})
	s, err := desktop.ReadSettings(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Pairbook != "https://pairbook.example.com" {
		t.Fatalf("stored Pairbook %q, want the earlier valid one", s.Pairbook)
	}
	_ = n
}

func TestStatusReachesTheRunningNodeWithoutRunningIt(t *testing.T) {
	dir := shortDir(t)
	if _, err := desktop.Connect(context.Background(), dir); err == nil {
		t.Fatal("status reached a node that nothing runs")
	}
	if _, err := os.Stat(filepath.Join(dir, "broker.db")); err == nil {
		t.Fatal("status created the node's database")
	}
	startAgent(t, desktop.Config{Dir: dir, Name: "box"})
	waitUntil(t, "the agent never ran the node", func() bool { return ownerRole(t, dir) == desktop.RoleAgent })
	o, _, _ := desktop.ReadOwnerInfo(dir)
	if o.PID != os.Getpid() || !o.Running() {
		t.Fatalf("owner record %+v, want this process, running", o)
	}
	n, err := desktop.Connect(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if n.Owner() {
		t.Fatal("status ran the node")
	}
	if got := selfName(t, n); got != "box" {
		t.Fatalf("name %q, want box", got)
	}
}
