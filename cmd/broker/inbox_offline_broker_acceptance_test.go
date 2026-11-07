//go:build linux || darwin

package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInboxOfflineBrokerProcess(t *testing.T) {
	if os.Getenv("PAIRTASK_OFFLINE_TEST_CHILD") != "1" {
		t.Skip("subprocess fixture only")
	}
	var cfg config
	if err := json.Unmarshal([]byte(os.Getenv("PAIRTASK_OFFLINE_TEST_CONFIG")), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := run(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil))); err != nil {
		t.Fatal(err)
	}
}

type offlineBrokerChild struct {
	cmd         *exec.Cmd
	done        chan error
	socket, log string
}

func newOfflineBrokerChild(t *testing.T, database, socket string) *offlineBrokerChild {
	t.Helper()
	cfg := config{DBPath: database, SocketPath: socket, OperatorSocketPath: socket + ".operator", OperatorUID: os.Getuid(), PairingSocketGID: -1}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	child := &offlineBrokerChild{cmd: exec.Command(os.Args[0], "-test.run=^TestInboxOfflineBrokerProcess$", "-test.timeout=20s"), done: make(chan error, 1), socket: socket, log: socket + ".log"}
	child.cmd.Env = append(os.Environ(), "PAIRTASK_OFFLINE_TEST_CHILD=1", "PAIRTASK_OFFLINE_TEST_CONFIG="+string(data))
	output, err := os.Create(child.log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { output.Close() })
	child.cmd.Stdout, child.cmd.Stderr = output, output
	if err := child.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { child.done <- child.cmd.Wait(); close(child.done) }()
	t.Cleanup(func() { child.stop(t) })
	return child
}

func (c *offlineBrokerChild) stop(t *testing.T) {
	t.Helper()
	_ = c.cmd.Process.Signal(os.Interrupt)
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		_ = c.cmd.Process.Kill()
		t.Error("temporary broker did not stop")
	}
}

func (c *offlineBrokerChild) ready(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(c.socket); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, _ := os.ReadFile(c.log)
	t.Fatalf("temporary broker did not start: %s", data)
}

func (c *offlineBrokerChild) refused(t *testing.T) {
	t.Helper()
	select {
	case err := <-c.done:
		if err == nil {
			t.Fatal("second broker exited successfully instead of refusing ownership")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second broker accepted the already-owned database")
	}
	data, _ := os.ReadFile(c.log)
	if !strings.Contains(strings.ToLower(string(data)), "lock") && !strings.Contains(strings.ToLower(string(data)), "in use") {
		t.Fatalf("second broker failed for another reason: %s", data)
	}
}

func TestInboxOfflineBrokerAcceptanceLifetimeOwnershipAndAliasRelease(t *testing.T) {
	dir, err := os.MkdirTemp("", "inbox-broker-owner-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	database := filepath.Join(dir, "state.db")
	first := newOfflineBrokerChild(t, database, filepath.Join(dir, "one.sock"))
	first.ready(t)
	alias := filepath.Join(dir, "alias.db")
	if err = os.Symlink(database, alias); err != nil {
		t.Fatal(err)
	}
	second := newOfflineBrokerChild(t, alias, filepath.Join(dir, "two.sock"))
	second.refused(t)
	first.stop(t)
	third := newOfflineBrokerChild(t, alias, filepath.Join(dir, "three.sock"))
	third.ready(t)
}
