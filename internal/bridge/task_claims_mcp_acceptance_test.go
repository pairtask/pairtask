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

func TestTaskClaimsMCPAcceptanceDiscoveryDoesNotConnect(t *testing.T) {
	path, _ := startBroker(t)
	session := connectMCP(t, lazyAgent(t, path, "worker"))
	listed, err := session.ListTools(context.Background(), nil)
	requireNoError(t, err)
	available := claimToolNames(listed.Tools)
	for _, name := range []string{"claim", "release_claim", "finish_claim", "read_claims"} {
		assertEqual(t, "claim tool "+name, available[name], true)
	}
	assertEqual(t, "discovery did not register agent", *brokerStatus(t, path).ConnectedSessions, 0)
}

func TestTaskClaimsMCPAcceptanceOldBrokerRefusesBeforeWriting(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 7
	session := connectMCP(t, c)
	for _, name := range []string{"claim", "release_claim", "finish_claim", "read_claims"} {
		args := map[string]any{"operator_message_id": "parent"}
		if name != "read_claims" {
			args["task"] = "reply"
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		cancel()
		requireNoError(t, err)
		if !result.IsError {
			t.Fatalf("%s succeeded against v7", name)
		}
		encoded, _ := json.Marshal(result.Content)
		if !strings.Contains(string(encoded), "unsupported") || !strings.Contains(string(encoded), "8") {
			t.Fatalf("%s didn't explain unsupported claims: %s", name, encoded)
		}
	}
	read := make(chan protocol.Message, 1)
	go func() {
		var msg protocol.Message
		_ = remote.SetReadDeadline(time.Now().Add(time.Second))
		_ = protocol.ReadFrame(bufio.NewReader(remote), &msg)
		read <- msg
	}()
	_, err := c.SendMessage(context.Background(), SendRequest{To: "peer", Text: "still connected", MessageID: "after-refusal"})
	requireNoError(t, err)
	assertEqual(t, "unsupported tools wrote no frames", (<-read).MessageID, "after-refusal")
}

func claimToolNames(tools []*mcp.Tool) map[string]bool {
	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name] = true
	}
	return names
}

func claimToolArgs(name string) map[string]any {
	args := map[string]any{"operator_message_id": "parent"}
	if name != "read_claims" {
		args["task"] = "reply"
	}
	return args
}

func replyToClaimTool(remote net.Conn, request chan<- protocol.Message) error {
	if err := remote.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	var req protocol.Message
	if err := protocol.ReadFrame(bufio.NewReader(remote), &req); err != nil {
		return err
	}
	request <- req
	if err := protocol.WriteFrame(remote, replyFrame("unrelated", "peer")); err != nil {
		return err
	}
	return json.NewEncoder(remote).Encode(map[string]any{"type": "claim_result", "request_id": req.RequestID, "operator_message_id": "parent", "claims": []any{map[string]any{"operator_message_id": "parent", "task": "reply", "topic": "Respond", "role": "owner", "agent_id": "worker", "state": "active", "claimed_at": "2026-10-25T00:00:00Z"}}, "claims_total": 1, "claims_truncated": false})
}

func requireClaimToolRequest(t *testing.T, req protocol.Message, kind string) {
	t.Helper()
	assertEqual(t, "wire operation", req.Type, kind)
	assertEqual(t, "parent separate from RPC", req.OperatorMessageID, "parent")
	if req.RequestID == "" {
		t.Fatal("missing RPC correlation")
	}
	if req.RequestID == "parent" {
		t.Fatal("RPC correlation reused parent identity")
	}
	switch kind {
	case "claim_set", "claim_release":
		assertEqual(t, "default role", req.Role, "owner")
	}
}

func requireConfirmedClaimResult(t *testing.T, result *mcp.CallToolResult) {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	requireNoError(t, err)
	var out struct {
		Parent string `json:"operator_message_id"`
		Claims []struct {
			Agent string `json:"agent_id"`
		} `json:"claims"`
		Total int `json:"claims_total"`
	}
	requireNoError(t, json.Unmarshal(data, &out))
	assertEqual(t, "confirmed parent", out.Parent, "parent")
	assertEqual(t, "confirmed total", out.Total, 1)
	assertEqual(t, "one confirmed holder", len(out.Claims), 1)
	assertEqual(t, "confirmed holder", out.Claims[0].Agent, "worker")
}

func TestTaskClaimsMCPAcceptanceCorrelatesToolsWithoutConsumingMessages(t *testing.T) {
	for _, tc := range []struct{ name, kind string }{{"claim", "claim_set"}, {"release_claim", "claim_release"}, {"finish_claim", "claim_done"}, {"read_claims", "claim_get"}} {
		t.Run(tc.name, func(t *testing.T) {
			c, remote := pipeClient(t)
			c.protocolVersion = 8
			session := connectMCP(t, c)
			request := make(chan protocol.Message, 1)
			done := make(chan error, 1)
			go func() { done <- replyToClaimTool(remote, request) }()
			result := call(t, session, tc.name, claimToolArgs(tc.name))
			requireNoError(t, <-done)
			requireClaimToolRequest(t, <-request, tc.kind)
			requireConfirmedClaimResult(t, result)
			inbox, err := c.Receive(context.Background(), 10, 0)
			requireNoError(t, err)
			assertEqual(t, "RPC response not leaked into inbox", len(inbox.Messages), 1)
			assertEqual(t, "unrelated message retained", inbox.Messages[0].MessageID, "unrelated-reply")
		})
	}
}

func TestTaskClaimsMCPAcceptanceConflictNamesWinnerAsError(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 8
	session := connectMCP(t, c)
	go func() {
		_ = remote.SetDeadline(time.Now().Add(2 * time.Second))
		var req protocol.Message
		if protocol.ReadFrame(bufio.NewReader(remote), &req) != nil {
			return
		}
		_ = json.NewEncoder(remote).Encode(map[string]any{"type": "error", "request_id": req.RequestID, "code": "claim_conflict", "detail": "already claimed", "operator_message_id": "parent", "claims": []any{map[string]any{"operator_message_id": "parent", "task": "reply", "role": "owner", "agent_id": "winning-agent", "state": "active", "claimed_at": "2026-10-25T00:00:00Z"}}, "claims_total": 1, "claims_truncated": false})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "claim", Arguments: map[string]any{"operator_message_id": "parent", "task": "reply"}})
	requireNoError(t, err)
	if !result.IsError {
		t.Fatal("losing claim reported success")
	}
	data, _ := json.Marshal(result)
	for _, want := range []string{"claim_conflict", "winning-agent"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("conflict omits %s: %s", want, data)
		}
	}
}
