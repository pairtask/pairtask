package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestDecisionMCPAcceptanceDiscoveryDoesNotConnect(t *testing.T) {
	path, _ := startBroker(t)
	session := connectMCP(t, lazyAgent(t, path, "worker"))
	listed, err := session.ListTools(context.Background(), nil)
	requireNoError(t, err)
	if !claimToolNames(listed.Tools)["request_decision"] {
		t.Fatal("request_decision was absent from tool discovery")
	}
	assertEqual(t, "discovery did not register agent", *brokerStatus(t, path).ConnectedSessions, 0)
}

func TestDecisionMCPAcceptanceOlderBrokerRefusesBeforeWriting(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = protocol.DecisionVersion - 1
	session := connectMCP(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "request_decision", Arguments: map[string]any{
		"summary": "Approve release", "key": "release",
	}})
	requireNoError(t, err)
	if !result.IsError {
		t.Fatal("old broker reported a decision request as sent")
	}
	encoded, _ := json.Marshal(result.Content)
	if !strings.Contains(string(encoded), "unsupported") || !strings.Contains(string(encoded), "27") {
		t.Fatalf("old-broker error was unclear: %s", encoded)
	}
	read := make(chan protocol.Message, 1)
	go func() {
		var msg protocol.Message
		_ = remote.SetReadDeadline(time.Now().Add(time.Second))
		_ = protocol.ReadFrame(bufio.NewReader(remote), &msg)
		read <- msg
	}()
	_, err = c.SendMessage(context.Background(), SendRequest{To: "peer", Text: "still connected", MessageID: "after-refusal"})
	requireNoError(t, err)
	assertEqual(t, "unsupported tool wrote no request frame", (<-read).MessageID, "after-refusal")
}

func replyToDecisionTool(remote net.Conn, request chan<- protocol.Message) error {
	if err := remote.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	var req protocol.Message
	if err := protocol.ReadFrame(bufio.NewReader(remote), &req); err != nil {
		return err
	}
	request <- req
	return json.NewEncoder(remote).Encode(map[string]any{
		"type": "decision_result", "request_id": req.RequestID,
		"decision": map[string]any{"id": "d-1", "agent": "worker", "key": "release", "anchor": "ask-1",
			"summary": "Approve release", "state": "open", "delivery": "pending", "created_at": "2026-11-10T00:00:00Z"},
	})
}

func TestDecisionMCPAcceptanceForwardsArgsAndCorrelatesResult(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = protocol.DecisionVersion
	session := connectMCP(t, c)
	request := make(chan protocol.Message, 1)
	done := make(chan error, 1)
	go func() { done <- replyToDecisionTool(remote, request) }()
	result := call(t, session, "request_decision", map[string]any{
		"summary": "Approve release", "key": "release", "message_id": "ask-1",
	})
	requireNoError(t, <-done)
	req := <-request
	if req.Type != protocol.TypeDecisionRequest || req.DecisionSummary != "Approve release" || req.DecisionKey != "release" || req.AnchorMessageID != "ask-1" || req.RequestID == "" {
		t.Fatalf("decision tool sent wrong frame: %+v", req)
	}
	data, err := json.Marshal(result.StructuredContent)
	requireNoError(t, err)
	var out protocol.Decision
	requireNoError(t, json.Unmarshal(data, &out))
	if out.ID != "d-1" || out.Anchor != "ask-1" || out.Delivery != protocol.DeliveryPending {
		t.Fatalf("decision tool returned wrong standing record: %+v", out)
	}
	inbox, err := c.Receive(context.Background(), 10, 0)
	requireNoError(t, err)
	if len(inbox.Messages) != 0 {
		t.Fatalf("decision RPC leaked into the message inbox: %+v", inbox.Messages)
	}
}
