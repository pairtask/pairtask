//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/desktop"
)

const sharedProcessDirEnv = "PAIRTASK_DESKTOP_SHARED_PROCESS_DIR"

// Re-exec this test binary so each MCP session has its own desktop.Start,
// database-lock handle and stdio transport, as Claude Desktop starts them.
func TestDesktopSharedNodeProcess(t *testing.T) {
	if dir := os.Getenv(sharedProcessDirEnv); dir != "" {
		log := slog.New(slog.NewTextHandler(os.Stderr, nil))
		if err := run([]string{"--dir", dir, "--name", "Claude Mac"}, log); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0) // Keep the testing package's PASS line off MCP stdout.
	}
	testDesktopSharedProcesses(t)
}

type desktopProcess struct {
	client *mcp.ClientSession
	cmd    *exec.Cmd
	logs   *lockedBuffer
	once   sync.Once
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

// Keep node.sock below macOS's Unix socket path limit.
func sharedProcessDir(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "pt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return filepath.Join(root, "node")
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func startDesktopProcess(t *testing.T, dir string) *desktopProcess {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestDesktopSharedNodeProcess$")
	cmd.Env = append(os.Environ(), sharedProcessDirEnv+"="+dir)
	logs := &lockedBuffer{}
	cmd.Stderr = logs
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "shared-desktop-process-test"}, nil).Connect(ctx,
		&mcp.CommandTransport{Command: cmd, TerminateDuration: 3 * time.Second}, nil)
	if err != nil {
		t.Fatalf("start desktop MCP process: %v; stderr: %s", err, logs.String())
	}
	p := &desktopProcess{client: client, cmd: cmd, logs: logs}
	t.Cleanup(p.close)
	return p
}

func (p *desktopProcess) close() {
	p.once.Do(func() { _ = p.client.Close() })
}

func processTool(t *testing.T, p *desktopProcess, name string, target any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := p.client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("%s through desktop process: %v; stderr: %s", name, err, p.logs.String())
	}
	if result.IsError {
		t.Fatalf("%s through desktop process was refused: %+v; stderr: %s", name, result.Content, p.logs.String())
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func processWhoami(t *testing.T, p *desktopProcess) desktop.WhoamiResult {
	t.Helper()
	var who desktop.WhoamiResult
	processTool(t, p, "whoami", &who)
	if who.Identity == "" {
		t.Fatal("desktop process has no workspace identity")
	}
	return who
}

func testDesktopSharedProcesses(t *testing.T) {
	t.Helper()
	dir := sharedProcessDir(t)
	first := startDesktopProcess(t, dir)
	want := processWhoami(t, first)
	second := startDesktopProcess(t, dir)
	if got := processWhoami(t, second); got != want {
		t.Fatalf("second Desktop process did not use first's node: got %+v, want %+v", got, want)
	}
	var status desktop.StatusResult
	processTool(t, second, "pair_status", &status)
	if status.Identity != want.Identity {
		t.Fatalf("second process's pairing status has identity %q, want %q", status.Identity, want.Identity)
	}
	first.close()
	if first.cmd.ProcessState == nil {
		t.Fatal("first Desktop process did not exit before takeover")
	}
	if got := processWhoami(t, second); got != want {
		t.Fatalf("second process lost the identity on takeover: got %+v, want %+v", got, want)
	}
	third := startDesktopProcess(t, dir)
	if got := processWhoami(t, third); got != want {
		t.Fatalf("third process did not use the new owner: got %+v, want %+v", got, want)
	}
}
