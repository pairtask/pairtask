//go:build linux

package bridge

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestReviewNotifyAcceptanceAssignmentReachesHostAndFallback(t *testing.T) {
	f := newReviewBridgeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	channel := &ChannelTransport{Transport: st}
	server, err := channel.Connect(ctx)
	requireNoError(t, err)
	defer server.Close()
	host, err := ct.Connect(ctx)
	requireNoError(t, err)
	defer host.Close()
	// Use the fixture's existing connection; the listener is its only consumer.
	lazy := &LazyClient{gate: make(chan struct{}, 1), client: f.client}
	listener := NewListener(ctx, lazy, channel)
	defer listener.Close()
	_, err = listener.Activate(ctx)
	requireNoError(t, err)
	f.request(nil)
	frame, err := host.Read(ctx)
	requireNoError(t, err)
	request, ok := frame.(*jsonrpc.Request)
	if !ok {
		t.Fatalf("host frame: %T", frame)
	}
	assertEqual(t, "host channel", request.Method, "notifications/claude/channel")
	var params struct{ Content string }
	requireNoError(t, json.Unmarshal(request.Params, &params))
	var msg protocol.Message
	requireNoError(t, json.Unmarshal([]byte(params.Content), &msg))
	assertEqual(t, "assignment identity", msg.ReviewID, "review-7")
	assertEqual(t, "durable notice identity", msg.MessageID, "review:review-7")
	assertEqual(t, "trusted operator provenance", msg.Origin, protocol.OriginOperator)
	assertEqual(t, "explicit acknowledgment required", msg.AckRequested, true)
	assertEqual(t, "durable delivery", msg.Durable, true)
	fallback, err := listener.Receive(ctx, 10, time.Second)
	requireNoError(t, err)
	assertEqual(t, "fallback contains assignment", len(fallback.Messages), 1)
	if !reflect.DeepEqual(fallback.Messages[0], msg) {
		t.Fatalf("host and fallback disagree: host=%+v fallback=%+v", msg, fallback.Messages)
	}
	assertEqual(t, "notification does not auto-accept review", f.view().State, "requested")
}
