package bridge

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func lazyAgent(t *testing.T, path, id string) *LazyClient {
	t.Helper()
	c, err := NewLazyClient(ClientConfig{SocketPath: path, AgentID: id, Harness: "test", Model: ""})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func waitForClientSignal(t *testing.T, signal <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(failure)
	}
}

func assertSinglePayload(t *testing.T, inbox Inbox, want string) {
	t.Helper()
	if len(inbox.Messages) != 1 {
		t.Fatalf("expected one buffered message: %+v", inbox)
	}
	if got := string(inbox.Messages[0].Payload); got != want {
		t.Fatalf("message payload = %s, want %s", got, want)
	}
}

func TestMCPDiscoveryWithoutBroker(t *testing.T) {
	c := lazyAgent(t, filepath.Join(t.TempDir(), "missing.sock"), "codex")
	session := connectMCP(t, c)
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil || len(listed.Tools) != 26 {
		t.Fatalf("discovery needs a broker: %+v %v", listed, err)
	}
	if c.client != nil {
		t.Fatal("discovery registered an agent")
	}
	// Configuration can be discovered while offline; the first actual tool
	// call must expose the connection error instead of claiming to be online.
	r, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "receive", Arguments: map[string]any{}})
	if err != nil || !r.IsError {
		t.Fatalf("missing broker not reported by tool: %+v %v", r, err)
	}
}

func TestMCPDiscoveryDoesNotEvictActiveAgent(t *testing.T) {
	path, _ := startBroker(t)
	active := lazyAgent(t, path, "codex")
	session := connectMCP(t, active)
	call(t, session, "receive", map[string]any{}) // Register the real session.
	// Reproduce Codex's initialize/list-tools/close inventory probe with the
	// same configured identity. It must never touch the broker.
	probe := lazyAgent(t, path, "codex")
	inventory := connectMCP(t, probe)
	if _, err := inventory.ListTools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	inventory.Close()
	probe.Close()
	if probe.client != nil {
		t.Fatal("inventory probe opened a broker connection")
	}
	if err := active.Send(context.Background(), "codex", "still connected"); err != nil {
		t.Fatal(err)
	}
	out := inboxResult(t, call(t, session, "wait", map[string]any{"timeout_seconds": 1}))
	if !out.Connected {
		t.Fatalf("inventory probe displaced the active session: %+v", out)
	}
	assertSinglePayload(t, out, `{"text":"still connected"}`)
}

func TestLazyClientConcurrentFirstCallsDialOnce(t *testing.T) {
	path, _ := startBroker(t)
	c := lazyAgent(t, path, "codex")
	var dials atomic.Int32
	dial := c.dial
	c.dial = func(ctx context.Context) (*Client, error) {
		dials.Add(1)
		return dial(ctx)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := c.Receive(context.Background(), 20, 0)
			if err != nil || !out.Connected {
				t.Errorf("first call failed: %+v %v", out, err)
			}
		}()
	}
	wg.Wait()
	if dials.Load() != 1 {
		t.Fatalf("dialed %d times", dials.Load())
	}
}

func TestLazyClientInitialDialCanRetry(t *testing.T) {
	path, _ := startBroker(t)
	c := lazyAgent(t, path, "codex")
	dial := c.dial
	initial := errors.New("temporary startup failure")
	c.dial = func(context.Context) (*Client, error) { return nil, initial }
	if _, err := c.Receive(context.Background(), 20, 0); !errors.Is(err, initial) {
		t.Fatalf("initial failure lost: %v", err)
	}
	c.dial = dial
	if out, err := c.Receive(context.Background(), 20, 0); err != nil || !out.Connected {
		t.Fatalf("initial failure prevented retry: %+v %v", out, err)
	}
}

func TestLazyClientDoesNotReconnectAfterDisconnect(t *testing.T) {
	path, stop := startBroker(t)
	c := lazyAgent(t, path, "codex")
	if _, err := c.Receive(context.Background(), 20, 0); err != nil {
		t.Fatal(err)
	}
	// Preserve a buffered message as well as the original disconnect error.
	if err := c.Send(context.Background(), "codex", "buffered"); err != nil {
		t.Fatal(err)
	}
	waitForClientSignal(t, c.client.notify, "message not buffered")
	stop()
	waitForClientSignal(t, c.client.done, "disconnect not observed")
	c.dial = func(context.Context) (*Client, error) {
		t.Fatal("silently reconnected after an established connection failed")
		return nil, nil
	}
	out, err := c.Receive(context.Background(), 20, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if out.Connected {
		t.Fatal("disconnected client reported connected")
	}
	if out.Error == "" {
		t.Fatal("disconnect error lost")
	}
	assertSinglePayload(t, out, `{"text":"buffered"}`)
	if err := c.Send(context.Background(), "claude", "late"); err == nil {
		t.Fatal("send succeeded after disconnect")
	}
}

func TestLazyClientCancellationAndCloseBeforeDial(t *testing.T) {
	c := lazyAgent(t, "/unused.sock", "codex")
	c.dial = func(context.Context) (*Client, error) {
		t.Fatal("canceled or closed client dialed")
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Receive(ctx, 20, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	c.Close()
	c.Close()
	if err := c.Send(context.Background(), "claude", "late"); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("closed client accepted a call: %v", err)
	}
}

func TestLazyClientWaitingCallerCanCancel(t *testing.T) {
	c := lazyAgent(t, "/unused.sock", "codex")
	started, release := make(chan struct{}), make(chan struct{})
	initial := errors.New("dial failed")
	c.dial = func(context.Context) (*Client, error) {
		close(started)
		<-release
		return nil, initial
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.Receive(context.Background(), 20, 0)
		done <- err
	}()
	defer func() {
		close(release)
		if err := <-done; !errors.Is(err, initial) {
			t.Errorf("first caller error: %v", err)
		}
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Receive(ctx, 20, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting caller cannot cancel: %v", err)
	}
}

func TestLazyClientValidatesIdentityBeforeDiscovery(t *testing.T) {
	for _, id := range []string{"", "*"} {
		if _, err := NewLazyClient(ClientConfig{SocketPath: "/unused.sock", AgentID: id, Harness: "test", Model: ""}); err == nil {
			t.Fatalf("accepted invalid identity %q", id)
		}
	}
}
