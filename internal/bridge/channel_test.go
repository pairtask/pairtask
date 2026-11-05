package bridge

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestChannelWireAndClosedHost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	channel := &ChannelTransport{Transport: st}
	server, err := channel.Connect(ctx)
	requireNoError(t, err)
	defer server.Close()
	client, err := ct.Connect(ctx)
	requireNoError(t, err)
	defer client.Close()
	msg := protocol.Message{Type: protocol.TypeMsg, MessageID: "review-1", From: "codex", SessionID: "peer-session", Payload: json.RawMessage(`{"text":"review context"}`)}
	written := make(chan error, 1)
	go func() { written <- channel.Publish(ctx, msg) }()
	frame, err := client.Read(ctx)
	requireNoError(t, err)
	requireNoError(t, <-written)
	notification, ok := frame.(*jsonrpc.Request)
	assertEqual(t, "notification", ok, true)
	assertEqual(t, "request ID absent", notification.ID.IsValid(), false)
	assertEqual(t, "method", notification.Method, "notifications/claude/channel")
	var params struct {
		Content string
		Meta    map[string]string
	}
	requireNoError(t, json.Unmarshal(notification.Params, &params))
	assertEqual(t, "peer provenance", params.Meta["sender"], "codex")
	var delivered protocol.Message
	requireNoError(t, json.Unmarshal([]byte(params.Content), &delivered))
	assertEqual(t, "message ID", delivered.MessageID, "review-1")
	client.Close()
	if channel.Publish(ctx, msg) == nil {
		t.Fatal("closed host reported successful submission")
	}
}

// discoveryDeadline bounds the MCP discovery handshake and calls, not the
// broker setup before them: CI's image builder runs the suite far slower.
const discoveryDeadline = 10 * time.Second

func TestChannelDiscoveryDoesNotOwnInbox(t *testing.T) {
	life, stop := context.WithCancel(context.Background())
	defer stop()
	path, _ := startBroker(t)
	owner := connectAgent(t, path, "worker")
	channel := &ChannelTransport{}
	l := NewListener(life, lazyAgent(t, path, "worker"), channel)
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), discoveryDeadline)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := NewHostMCP(l, true).Connect(ctx, st, nil)
	requireNoError(t, err)
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "discovery"}, nil).Connect(ctx, ct, nil)
	requireNoError(t, err)
	defer cs.Close()
	_, err = cs.ListTools(ctx, nil)
	requireNoError(t, err)
	call(t, cs, "listener_status", map[string]any{})
	assertEqual(t, "inactive discovery", l.Status().State, "inactive")
	assertEqual(t, "original owner healthy", owner.connectionError(), nil)
	if cs.InitializeResult().Capabilities.Experimental["claude/channel"] == nil {
		t.Fatal("channel capability missing")
	}
	_, err = l.Activate(ctx)
	assertErrorContains(t, err, protocol.ErrDuplicateID)
}

func TestChannelBlockedWriteCancels(t *testing.T) {
	in, send := io.Pipe()
	out, receive := io.Pipe()
	defer send.Close()
	defer out.Close()
	channel := &ChannelTransport{Transport: &mcp.IOTransport{Reader: in, Writer: receive}}
	server, err := channel.Connect(context.Background())
	requireNoError(t, err)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- channel.Publish(ctx, protocol.Message{Type: protocol.TypeMsg}) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unread host write succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked write ignored cancellation")
	}
}
