//go:build linux || darwin

package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/desktop"
)

type daemonProcess struct {
	cmd  *exec.Cmd
	log  *lockedBuffer
	done chan struct{}
	err  error
}

func startDaemonProcess(t *testing.T, dir string) *daemonProcess {
	t.Helper()
	d := &daemonProcess{cmd: desktopTestCommand(t, dir, "daemon"), log: &lockedBuffer{}, done: make(chan struct{})}
	d.cmd.Stderr = d.log
	// No MCP client, stdin pipe or terminal is attached.
	if err := d.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { d.err = d.cmd.Wait(); close(d.done) }()
	t.Cleanup(func() {
		select {
		case <-d.done:
		default:
			_ = d.cmd.Process.Kill()
			<-d.done
		}
	})
	return d
}

func (d *daemonProcess) waitOwner(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-d.done:
			t.Fatalf("daemon exited: %v (%s)", d.err, d.log.String())
		default:
		}
		conn, err := net.DialTimeout("unix", filepath.Join(dir, desktop.SocketFile), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("daemon never listened: %s", d.log.String())
}

func (d *daemonProcess) terminate(t *testing.T, dir string) {
	t.Helper()
	if err := d.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-d.done:
		if d.err != nil {
			t.Fatalf("SIGTERM: %v (%s)", d.err, d.log.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon failed to stop on SIGTERM")
	}
	if _, err := os.Lstat(filepath.Join(dir, desktop.SocketFile)); !os.IsNotExist(err) {
		t.Fatalf("owner socket after SIGTERM: %v", err)
	}
}

func TestDaemonRetainsOwnershipWithoutAnMCPClient(t *testing.T) {
	dir := sharedProcessDir(t)
	d := startDaemonProcess(t, dir)
	d.waitOwner(t, dir)
	client := startMCPProcess(t, dir, "mcp")
	want := processWhoami(t, client)
	client.close()
	d.waitOwner(t, dir)
	reopened := startMCPProcess(t, dir, "mcp")
	var status desktop.StatusResult
	processTool(t, reopened, "pair_status", &status)
	if status.Identity != want.Identity {
		t.Fatalf("reconnected client identity %q, want %q", status.Identity, want.Identity)
	}
	reopened.close()
	d.terminate(t, dir)
	next := startDesktopProcess(t, dir)
	if got := processWhoami(t, next); got.Identity != want.Identity {
		t.Fatalf("identity after daemon shutdown %q, want %q", got.Identity, want.Identity)
	}
}

func TestDaemonTakesOverFromExistingMCPOwner(t *testing.T) {
	dir := sharedProcessDir(t)
	owner := startDesktopProcess(t, dir)
	want := processWhoami(t, owner)
	d := startDaemonProcess(t, dir)
	deadline := time.Now().Add(8 * time.Second)
	for !strings.Contains(d.log.String(), "another copy runs the broker") {
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not wait for existing owner: %s", d.log.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := processWhoami(t, owner); got != want {
		t.Fatalf("existing session changed: %+v", got)
	}
	owner.close()
	d.waitOwner(t, dir)
	client := startMCPProcess(t, dir, "mcp")
	if got := processWhoami(t, client); got != want {
		t.Fatalf("identity across takeover: %+v, want %+v", got, want)
	}
	client.close()
	d.terminate(t, dir)
}
