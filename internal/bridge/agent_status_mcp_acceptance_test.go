package bridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/protocol"
)

func statusMCPRecord(t *testing.T, result *mcp.CallToolResult) *statusRecordView {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	requireNoError(t, err)
	var out struct {
		Status *statusRecordView `json:"agent_status"`
	}
	requireNoError(t, json.Unmarshal(data, &out))
	return out.Status
}

func TestAgentStatusMCPAcceptanceConfirmedWriteReadAndValidation(t *testing.T) {
	path, _ := startBroker(t)
	client := lazyAgent(t, path, "worker")
	session := connectMCP(t, client)
	listed, err := session.ListTools(context.Background(), nil)
	requireNoError(t, err)
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	if !names["status"] || !names["read_status"] {
		t.Fatal("status tools absent from discovery")
	}
	assertEqual(t, "discovery did not connect", *brokerStatus(t, path).ConnectedSessions, 0)
	first := statusMCPRecord(t, call(t, session, "status", map[string]any{"state": "active", "task": "#133", "step": "tests"}))
	if first == nil || first.AgentID != "worker" || first.Task != "#133" || !first.Connected {
		t.Fatalf("status not broker-confirmed: %+v", first)
	}
	read := statusMCPRecord(t, call(t, session, "read_status", map[string]any{}))
	if read == nil || read.SessionID != first.SessionID || !read.DeclaredAt.Equal(first.DeclaredAt) {
		t.Fatalf("self read differs from confirmed write: %+v", read)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	bad, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "status", Arguments: map[string]any{"state": "approved"}})
	requireNoError(t, err)
	if !bad.IsError {
		t.Fatal("invalid status tool call reported success")
	}
	after := statusMCPRecord(t, call(t, session, "read_status", map[string]any{}))
	if after == nil || after.State != "active" || !after.DeclaredAt.Equal(first.DeclaredAt) {
		t.Fatalf("failed tool call changed status: %+v", after)
	}
	if unknown := statusMCPRecord(t, call(t, session, "read_status", map[string]any{"agent_id": "unknown"})); unknown != nil {
		t.Fatalf("unknown status fabricated: %+v", unknown)
	}
	inbox := inboxResult(t, call(t, session, "receive", map[string]any{"limit": 100}))
	if len(inbox.Messages) != 0 {
		t.Fatalf("status RPCs leaked into inbox: %+v", inbox.Messages)
	}
}

func TestAgentStatusMCPAcceptanceActiveListenerKeepsOwnerAndPendingMessage(t *testing.T) {
	path, _ := startBroker(t)
	publisher := &testPublisher{messages: make(chan protocol.Message, 4)}
	listener := NewListener(context.Background(), lazyAgent(t, path, "worker"), publisher)
	t.Cleanup(listener.Close)
	session := connectAutoChannel(t, listener)
	awaitListener(t, listener, func() bool { return listener.started })
	owner := listener.Status().SessionID
	peer := connectAgent(t, path, "peer")
	sendTracked(t, peer, SendRequest{To: "worker", Text: "review", MessageID: "status-pending"})
	assertEqual(t, "pending message delivered", awaitPublished(t, publisher).MessageID, "status-pending")
	first := statusMCPRecord(t, call(t, session, "status", map[string]any{"state": "waiting", "task": "#133", "waiting_on": "peer"}))
	if first == nil || first.SessionID != owner {
		t.Fatalf("status opened another owner: %+v", first)
	}
	call(t, session, "read_status", map[string]any{})
	assertEqual(t, "listener owner unchanged", listener.Status().SessionID, owner)
	assertEqual(t, "status did not acknowledge pending work", brokerStatus(t, path).DurablePending.Total, 1)
	inbox := inboxResult(t, call(t, session, "receive", map[string]any{"limit": 100}))
	found := false
	for _, frame := range inbox.Messages {
		if frame.Type == "agent_status_result" {
			t.Fatal("status result entered listener fallback queue")
		}
		if frame.MessageID == "status-pending" && frame.Type == protocol.TypeMsg {
			found = true
		}
	}
	if !found {
		t.Fatal("status RPC consumed unrelated pending message")
	}
}

func TestAgentStatusMCPAcceptanceOlderBrokerFailsExplicitlyWithoutDisconnect(t *testing.T) {
	client, remote := pipeClient(t)
	client.protocolVersion = 5 // Older v5 brokers do not advertise status support.
	session := connectMCP(t, client)
	for _, name := range []string{"status", "read_status"} {
		args := map[string]any{}
		if name == "status" {
			args["state"] = "active"
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		cancel()
		requireNoError(t, err)
		if !result.IsError {
			t.Fatalf("%s claimed support on an old broker", name)
		}
		data, err := json.Marshal(result.Content)
		requireNoError(t, err)
		if !strings.Contains(string(data), "unsupported") {
			t.Fatalf("%s did not explain missing support: %s", name, data)
		}
	}
	// The old messaging transport remains useful after unsupported status calls.
	requireNoError(t, remote.SetWriteDeadline(time.Now().Add(time.Second)))
	requireNoError(t, protocol.WriteFrame(remote, replyFrame("still-live", "peer")))
	assertEqual(t, "messaging survived unsupported status", nextFrame(t, client).MessageID, "still-live-reply")
}

func TestAgentStatusClaudeActivityComesFromToolCallsNotIncomingMessages(t *testing.T) {
	path, _ := startBroker(t)
	publisher := &testPublisher{messages: make(chan protocol.Message, 4)}
	listener := NewListener(context.Background(), lazyAgent(t, path, "worker"), publisher)
	t.Cleanup(listener.Close)
	session := connectAutoChannel(t, listener)
	awaitListener(t, listener, func() bool { return listener.started })
	_, err := listener.SetStatus(context.Background(), protocol.AgentStatus{State: "active", Task: "#133"})
	requireNoError(t, err)
	observer := statusWire(t, path, "observer")
	peer := connectAgent(t, path, "peer")
	sendTracked(t, peer, SendRequest{To: "worker", Text: "notification only", MessageID: "not-activity"})
	assertEqual(t, "host notification", awaitPublished(t, publisher).MessageID, "not-activity")
	before := statusGet(t, observer, "worker")
	if before == nil {
		t.Fatal("missing fixture declaration")
	}
	if len(before.Activity) != 0 && string(before.Activity) != "null" {
		t.Fatalf("incoming notification fabricated harness activity: %s", before.Activity)
	}
	call(t, session, "read_status", map[string]any{})
	deadline := time.Now().Add(time.Second)
	for {
		got := statusGet(t, observer, "worker")
		var activity struct {
			Source string `json:"source"`
		}
		if got != nil && len(got.Activity) > 0 {
			requireNoError(t, json.Unmarshal(got.Activity, &activity))
			if activity.Source == "claude-adapter" {
				if !got.DeclaredAt.Equal(before.DeclaredAt) {
					t.Fatal("tool activity refreshed declared milestone")
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("tool invocation did not produce attributed adapter activity: %+v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
