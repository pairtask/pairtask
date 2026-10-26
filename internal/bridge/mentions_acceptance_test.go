package bridge

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestMentionsAcceptanceClaudeChannelPreservesAttentionAndOrigin(t *testing.T) {
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
	input := protocol.Message{Type: protocol.TypeMsg, From: "operator", Origin: protocol.OriginOperator,
		To: "*", Room: "alpha", MessageID: "attention-channel", Mentions: []string{"worker with spaces", "worker-two"},
		Payload: json.RawMessage(`{"text":"please review"}`), AckRequested: true}
	done := make(chan error, 1)
	go func() { done <- channel.Publish(ctx, input) }()
	frame, err := client.Read(ctx)
	requireNoError(t, err)
	requireNoError(t, <-done)
	request, ok := frame.(*jsonrpc.Request)
	if !ok {
		t.Fatalf("unexpected channel frame: %T", frame)
	}
	var params struct {
		Content string
		Meta    map[string]string
	}
	requireNoError(t, json.Unmarshal(request.Params, &params))
	var delivered protocol.Message
	requireNoError(t, json.Unmarshal([]byte(params.Content), &delivered))
	if !reflect.DeepEqual(delivered, input) {
		t.Fatalf("channel lost attention or provenance: %+v", delivered)
	}
}

func TestMentionsAcceptanceManualReceivePreservesAttention(t *testing.T) {
	c, remote := pipeClient(t)
	remote.SetWriteDeadline(time.Now().Add(time.Second))
	requireNoError(t, protocol.WriteFrame(remote, protocol.Message{Type: protocol.TypeMsg, From: "operator", Origin: protocol.OriginOperator,
		MessageID: "attention-receive", Mentions: []string{"worker"}, Payload: json.RawMessage(`{"text":"review"}`)}))
	out, err := c.Receive(context.Background(), 1, time.Second)
	requireNoError(t, err)
	if len(out.Messages) != 1 || !slices.Equal(out.Messages[0].Mentions, []string{"worker"}) {
		t.Fatalf("receive lost attention: %+v", out)
	}
}
