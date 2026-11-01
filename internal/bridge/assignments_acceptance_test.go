package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestAssignmentsBridgeAcceptanceTypedToolSchemaAndGuidance(t *testing.T) {
	path, _ := startBroker(t)
	session := connectMCP(t, lazyAgent(t, path, "worker"))
	listed, err := session.ListTools(context.Background(), nil)
	requireNoError(t, err)
	found := false
	for _, tool := range listed.Tools {
		if tool.Name != "read_claims" {
			continue
		}
		found = true
		for _, word := range []string{"assignment", "assigned", "released", "done", "release_claim", "finish_claim"} {
			if !strings.Contains(tool.Description, word) {
				t.Errorf("read_claims guidance omits %q: %s", word, tool.Description)
			}
		}
		schema, err := json.Marshal(tool.OutputSchema)
		requireNoError(t, err)
		for _, field := range []string{"assignments", "assignments_total", "assignments_truncated", "agent_id", "assigned_at", "state"} {
			if !strings.Contains(string(schema), `"`+field+`"`) {
				t.Errorf("typed output schema omits %s: %s", field, schema)
			}
		}
	}
	if !found {
		t.Fatal("read_claims missing")
	}
	assertEqual(t, "discovery remains lazy", *brokerStatus(t, path).ConnectedSessions, 0)
}

func TestAssignmentsBridgeAcceptanceToolKeepsTypedAssignmentsSeparateFromClaims(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 16
	session := connectMCP(t, c)
	done := make(chan error, 1)
	go func() {
		if err := remote.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			done <- err
			return
		}
		var request protocol.Message
		if err := protocol.ReadFrame(bufio.NewReader(remote), &request); err != nil {
			done <- err
			return
		}
		frame := map[string]any{"type": "claim_result", "request_id": request.RequestID, "operator_message_id": "parent",
			"claims": []any{map[string]any{"operator_message_id": "parent", "agent_id": "reviewer", "task": "reply", "role": "reviewer", "state": "active", "claimed_at": "2026-11-01T00:00:00Z"}}, "claims_total": 1, "claims_truncated": false,
			"assignments": []any{map[string]any{"operator_message_id": "parent", "agent_id": "worker", "task": "reply", "state": "assigned", "assigned_at": "2026-11-01T00:00:00Z"},
				map[string]any{"operator_message_id": "parent", "agent_id": "other", "task": "reply", "state": "released", "assigned_at": "2026-11-01T00:00:00Z", "finished_at": "2026-11-01T01:00:00Z"}}, "assignments_total": 2, "assignments_truncated": false}
		done <- json.NewEncoder(remote).Encode(frame)
	}()
	result := call(t, session, "read_claims", map[string]any{"operator_message_id": "parent"})
	requireNoError(t, <-done)
	if result.IsError {
		t.Fatalf("assignment read refused: %+v", result)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	requireNoError(t, err)
	var view struct {
		Assignments []struct {
			Agent string `json:"agent_id"`
			Task  string `json:"task"`
			State string `json:"state"`
		} `json:"assignments"`
		Total       int               `json:"assignments_total"`
		Truncated   *bool             `json:"assignments_truncated"`
		Claims      []json.RawMessage `json:"claims"`
		ClaimsTotal int               `json:"claims_total"`
	}
	requireNoError(t, json.Unmarshal(encoded, &view))
	assertEqual(t, "typed assignment total", view.Total, 2)
	assertEqual(t, "typed assignment list", len(view.Assignments), 2)
	assertEqual(t, "claims remain separate", view.ClaimsTotal, 1)
	assertEqual(t, "claims list untouched", len(view.Claims), 1)
	if view.Truncated == nil || *view.Truncated {
		t.Fatalf("explicit false assignment truncation missing: %s", encoded)
	}
	assertEqual(t, "assigned identity retained", view.Assignments[0].Agent, "worker")
	assertEqual(t, "canonical reply retained", view.Assignments[0].Task, "reply")
	assertEqual(t, "assigned state retained", view.Assignments[0].State, "assigned")
	assertEqual(t, "released state retained", view.Assignments[1].State, "released")
}

func TestAssignmentsBridgeAcceptanceDeliveredMetadataSurvivesReceive(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 16
	done := make(chan error, 1)
	go func() {
		done <- json.NewEncoder(remote).Encode(map[string]any{"type": "msg", "message_id": "assigned-delivery", "origin": "operator", "from": "operator", "to": "*", "assignment": map[string]string{"task": "reply", "state": "assigned"}, "payload": map[string]string{"text": "answer here"}})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := c.Receive(ctx, 10, 2*time.Second)
	requireNoError(t, err)
	requireNoError(t, <-done)
	assertEqual(t, "one delivered assignment", len(result.Messages), 1)
	encoded, err := json.Marshal(result.Messages[0])
	requireNoError(t, err)
	var frame struct {
		Origin     string            `json:"origin"`
		Assignment map[string]string `json:"assignment"`
	}
	requireNoError(t, json.Unmarshal(encoded, &frame))
	assertEqual(t, "broker operator origin retained", frame.Origin, "operator")
	assertEqual(t, "assignment task retained", frame.Assignment["task"], "reply")
	assertEqual(t, "assignment state retained", frame.Assignment["state"], "assigned")
}

func TestAssignmentsBridgeAcceptanceClaimDefaultsOwnerBeforeWireAdmission(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 16
	session := connectMCP(t, c)
	requests := make(chan protocol.Message, 1)
	done := make(chan error, 1)
	go func() {
		var request protocol.Message
		if err := protocol.ReadFrame(bufio.NewReader(remote), &request); err != nil {
			done <- err
			return
		}
		requests <- request
		done <- json.NewEncoder(remote).Encode(map[string]any{
			"type": "claim_result", "request_id": request.RequestID, "operator_message_id": "parent",
			"claims": []any{}, "claims_total": 0, "claims_truncated": false,
			"assignments":       []any{map[string]any{"operator_message_id": "parent", "agent_id": "worker", "task": "reply", "state": "assigned", "assigned_at": "2026-11-01T00:00:00Z"}},
			"assignments_total": 1, "assignments_truncated": false,
		})
	}()
	result := call(t, session, "claim", map[string]any{"operator_message_id": "parent", "task": "reply"})
	requireNoError(t, <-done)
	request := <-requests
	assertEqual(t, "bridge supplies raw-wire owner", request.Role, "owner")
	assertEqual(t, "canonical task remains reply", request.Task, "reply")
	if result.IsError {
		t.Fatal("default-owner assignment claim refused")
	}
}
