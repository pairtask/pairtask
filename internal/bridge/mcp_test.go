package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/server"
	"github.com/pairtask/pairtask/internal/store"
)

func startBroker(t *testing.T) (string, context.CancelFunc) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "collab-mcp-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "broker.sock")
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := hub.New(st, 0, log)
	go h.Run(ctx)
	srv := server.New(path, h, log)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Listen(ctx); err != nil {
			t.Error(err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("server shutdown timed out")
		}
		select {
		case <-h.Done():
		case <-time.After(3 * time.Second):
			t.Error("hub shutdown timed out")
		}
		st.Close()
	})
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return path, cancel
}

func connectAgent(t *testing.T, path, id string) *Client {
	t.Helper()
	c, err := Dial(context.Background(), ClientConfig{SocketPath: path, AgentID: id, Harness: "test", Model: ""})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func connectMCP(t *testing.T, c messagingClient) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ss, err := NewMCP(c).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, c *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := c.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("%s failed: %+v", name, result.Content)
	}
	return result
}

func inboxResult(t *testing.T, r *mcp.CallToolResult) Inbox {
	t.Helper()
	data, err := json.Marshal(r.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out Inbox
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// awaitMCPFrame consumes a single frame at a time so later stages stay observable.
func awaitMCPFrame(t *testing.T, c *mcp.ClientSession) protocol.Message {
	t.Helper()
	out := inboxResult(t, call(t, c, "wait", map[string]any{"timeout_seconds": 1, "limit": 1}))
	if !out.Connected || len(out.Messages) != 1 {
		t.Fatalf("expected one frame: %+v", out)
	}
	return out.Messages[0]
}

func assertStage(t *testing.T, msg protocol.Message, id, stage string) {
	t.Helper()
	assertEqual(t, "frame type", msg.Type, protocol.TypeAck)
	assertEqual(t, "message ID", msg.MessageID, id)
	assertEqual(t, "acknowledgment stage", msg.Stage, stage)
}

func TestMCPToolDiscovery(t *testing.T) {
	path, _ := startBroker(t)
	codex := connectMCP(t, lazyAgent(t, path, "codex"))
	listed, err := codex.ListTools(context.Background(), nil)
	requireNoError(t, err)
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	assertEqual(t, "tool count", len(names), 10)
	for _, name := range []string{"send", "receive", "wait", "wait_reply", "acknowledge", "delegate_listener", "wait_delegated", "revoke_listener", "status", "read_status"} {
		assertEqual(t, name, names[name], true)
	}
}

func TestMCPExchange(t *testing.T) {
	path, _ := startBroker(t)
	codex := connectMCP(t, lazyAgent(t, path, "codex"))
	claude := connectMCP(t, lazyAgent(t, path, "claude"))
	requestID := sendMCPReview(t, claude, codex)
	// Transport receipt must never turn into an automatic agent acknowledgment.
	empty := inboxResult(t, call(t, claude, "wait", map[string]any{"timeout_seconds": 1}))
	assertEqual(t, "unexpected acknowledgment", len(empty.Messages), 0)
	assertEqual(t, "wait timed out", empty.TimedOut, true)
	acknowledgeMCPReview(t, claude, codex, requestID)
	call(t, codex, "send", map[string]any{"to": "claude", "text": "reviewed: add a disconnect test", "message_id": "review-reply", "in_reply_to": requestID})
	assertStage(t, awaitMCPFrame(t, codex), "review-reply", protocol.StageAccepted)
	reply := awaitMCPFrame(t, claude)
	assertEqual(t, "reply correlation", reply.InReplyTo, requestID)
	assertEqual(t, "reply ID", reply.MessageID, "review-reply")
	assertEqual(t, "reply sender", reply.From, "codex")
	assertStage(t, awaitMCPFrame(t, codex), "review-reply", protocol.StageAdapterReceived)
	acknowledgeMCPReview(t, codex, claude, "review-reply")
}

func sendMCPReview(t *testing.T, claude, codex *mcp.ClientSession) string {
	t.Helper()
	registered := inboxResult(t, call(t, codex, "receive", map[string]any{}))
	assertEqual(t, "missing session ID", registered.SessionID == "", false)
	assertEqual(t, "acknowledgments supported", registered.AcknowledgmentsSupported, true)
	sent := call(t, claude, "send", map[string]any{"to": "codex", "text": "review ready"})
	data, err := json.Marshal(sent.StructuredContent)
	requireNoError(t, err)
	var result SendResult
	requireNoError(t, json.Unmarshal(data, &result))
	assertEqual(t, "missing message ID", result.MessageID == "", false)
	assertEqual(t, "send status", result.Status, "written")
	assertEqual(t, "delivery confirmed", result.DeliveryConfirmed, false)
	accepted := awaitMCPFrame(t, claude)
	assertStage(t, accepted, result.MessageID, protocol.StageAccepted)
	assertEqual(t, "recipient count", len(accepted.Recipients), 1)
	assertEqual(t, "recipient", accepted.Recipients[0], protocol.Recipient{AgentID: "codex", SessionID: registered.SessionID})
	request := awaitMCPFrame(t, codex)
	assertEqual(t, "request type", request.Type, protocol.TypeMsg)
	assertEqual(t, "request sender", request.From, "claude")
	assertEqual(t, "request ID", request.MessageID, result.MessageID)
	assertEqual(t, "request payload", string(request.Payload), `{"text":"review ready"}`)
	assertStage(t, awaitMCPFrame(t, claude), result.MessageID, protocol.StageAdapterReceived)
	return result.MessageID
}

func acknowledgeMCPReview(t *testing.T, sender, recipient *mcp.ClientSession, id string) {
	t.Helper()
	call(t, recipient, "acknowledge", map[string]any{"message_id": id})
	assertStage(t, awaitMCPFrame(t, recipient), id, protocol.StageAgentAcknowledged)
	assertStage(t, awaitMCPFrame(t, sender), id, protocol.StageAgentAcknowledged)
}

func TestMCPUnknownRecipient(t *testing.T) {
	path, _ := startBroker(t)
	claude := connectMCP(t, lazyAgent(t, path, "claude"))
	call(t, claude, "send", map[string]any{"to": "missing", "text": "hello", "message_id": "missing-request"})
	routingError := awaitMCPFrame(t, claude)
	assertEqual(t, "routing error", routingError.Code, protocol.ErrUnknownRecipient)
	assertEqual(t, "failed message ID", routingError.MessageID, "missing-request")
}

func TestMCPInvalidArguments(t *testing.T) {
	path, _ := startBroker(t)
	codex := connectMCP(t, lazyAgent(t, path, "codex"))
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"send", map[string]any{"to": "codex"}},
		{"receive", map[string]any{"limit": 101}},
		{"wait", map[string]any{"timeout_seconds": 31}},
		{"acknowledge", map[string]any{"message_id": ""}},
	} {
		r, err := codex.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
		if err == nil && !r.IsError {
			t.Fatalf("accepted invalid arguments for %s", tc.name)
		}
	}
}

func TestReceiveTimeoutCancellationAndDisconnect(t *testing.T) {
	path, stop := startBroker(t)
	c := connectAgent(t, path, "codex")
	out, err := c.Receive(context.Background(), 20, 10*time.Millisecond)
	if err != nil || !out.TimedOut || !out.Connected {
		t.Fatalf("bad timeout: %+v %v", out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Receive(ctx, 20, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	if err := c.Send(context.Background(), "codex", "buffered"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.notify:
	case <-time.After(time.Second):
		t.Fatal("socket reader did not queue message")
	}
	stop()
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("disconnect not detected")
	}
	out, err = c.Receive(context.Background(), 20, time.Second)
	if err != nil || out.Connected || out.Error == "" || len(out.Messages) != 1 {
		t.Fatalf("buffered frame or disconnect status lost: %+v %v", out, err)
	}
	if err := c.Send(context.Background(), "claude", "late"); err == nil {
		t.Fatal("send succeeded after disconnect")
	}
}

func pipeClient(t *testing.T) (*Client, net.Conn) {
	t.Helper()
	local, remote := net.Pipe()
	c := &Client{conn: local, writeGate: make(chan struct{}, 1), notify: make(chan struct{}, 1), done: make(chan struct{}), readDone: make(chan struct{})}
	go c.readLoop(bufio.NewReader(local))
	t.Cleanup(c.Close)
	t.Cleanup(func() { remote.Close() })
	return c, remote
}

func TestInboxOverflowIsBoundedAndReported(t *testing.T) {
	c, remote := pipeClient(t)
	payload, _ := json.Marshal(strings.Repeat("x", protocol.MaxFrameBytes-100))
	remote.SetWriteDeadline(time.Now().Add(2 * time.Second))
	for i := 0; i < 5; i++ {
		if err := protocol.WriteFrame(remote, protocol.Message{Type: protocol.TypeMsg, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("overflow did not disconnect")
	}
	out, err := c.Receive(context.Background(), 20, 0)
	if err != nil || out.Connected || !strings.Contains(out.Error, "overflow") || len(out.Messages) != 4 {
		t.Fatalf("overflow was hidden: count=%d error=%s err=%v", len(out.Messages), out.Error, err)
	}
}

func TestCanceledSocketWriteReturns(t *testing.T) {
	c, _ := pipeClient(t) // Peer never reads our write.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.Send(ctx, "claude", "hello"); err == nil {
		t.Fatal("blocked write unexpectedly succeeded")
	}
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("failed write left connection open")
	}
}
