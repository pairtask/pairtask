package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestAgentRolesMCPAcceptanceOlderBrokerRefusesWithoutWriting(t *testing.T) {
	client, remote := pipeClient(t)
	client.protocolVersion = protocol.RoleVersion - 1
	session := connectMCP(t, client)
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{{"set_role", map[string]any{"role": "CI tests and reviews"}}, {"classifier_feedback", map[string]any{}}} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
		cancel()
		requireNoError(t, err)
		if !result.IsError {
			t.Fatalf("%s claimed support on a v27 broker", tc.tool)
		}
		data, err := json.Marshal(result.Content)
		requireNoError(t, err)
		if !strings.Contains(string(data), "unsupported") {
			t.Fatalf("%s did not explain the old broker: %s", tc.tool, data)
		}
	}
	// A refused role request must not poison the existing messaging connection.
	request := make(chan protocol.Message, 1)
	go func() {
		var msg protocol.Message
		_ = remote.SetReadDeadline(time.Now().Add(time.Second))
		_ = protocol.ReadFrame(bufio.NewReader(remote), &msg)
		request <- msg
	}()
	_, err := client.SendMessage(context.Background(), SendRequest{To: "peer", Text: "still connected", MessageID: "after-role-refusal"})
	requireNoError(t, err)
	if got := <-request; got.MessageID != "after-role-refusal" {
		t.Fatalf("unsupported role tool wrote a frame or lost messaging: %+v", got)
	}
}

func TestAgentRolesMCPAcceptanceSetAndFeedbackUseCorrelatedFrames(t *testing.T) {
	client, remote := pipeClient(t)
	client.protocolVersion = protocol.RoleVersion
	session := connectMCP(t, client)
	requests := make(chan protocol.Message, 2)
	done := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(remote)
		for _, reply := range []protocol.Message{
			{Type: protocol.TypeRoleResult, AgentRole: &protocol.AgentRole{AgentID: "worker", Role: "CI tests and reviews"}},
			{Type: protocol.TypeClassifierFeedbackResult, ClassifierFeedback: &protocol.ClassifierFeedback{AgentID: "worker", Role: "CI tests and reviews", Enabled: true, Counts: map[string]int{"missed": 1}, Items: []protocol.FeedbackItem{{MessageID: "ask-1", Verdict: protocol.VerdictMissed}}}},
		} {
			var request protocol.Message
			if err := protocol.ReadFrame(reader, &request); err != nil {
				done <- err
				return
			}
			requests <- request
			reply.RequestID = request.RequestID
			if err := protocol.WriteFrame(remote, reply); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	set := call(t, session, "set_role", map[string]any{"role": "CI tests and reviews"})
	var setResult struct {
		Role protocol.AgentRole `json:"agent_role"`
	}
	decodeRoleTool(t, set.StructuredContent, &setResult)
	if setResult.Role.AgentID != "worker" || setResult.Role.Role != "CI tests and reviews" {
		t.Fatalf("set_role lost the broker-confirmed role: %+v", setResult)
	}
	feedback := call(t, session, "classifier_feedback", map[string]any{"limit": 7})
	var feedbackResult struct {
		Feedback protocol.ClassifierFeedback `json:"classifier_feedback"`
	}
	decodeRoleTool(t, feedback.StructuredContent, &feedbackResult)
	if feedbackResult.Feedback.AgentID != "worker" || len(feedbackResult.Feedback.Items) != 1 || feedbackResult.Feedback.Items[0].Verdict != protocol.VerdictMissed {
		t.Fatalf("feedback lost the missed-claim evidence: %+v", feedbackResult)
	}
	requireNoError(t, <-done)
	setRequest, feedbackRequest := <-requests, <-requests
	if setRequest.Type != protocol.TypeRoleSet || setRequest.AgentRoleText != "CI tests and reviews" || setRequest.RequestID == "" {
		t.Fatalf("set_role sent the wrong frame: %+v", setRequest)
	}
	if feedbackRequest.Type != protocol.TypeClassifierFeedbackGet || feedbackRequest.FeedbackLimit != 7 || feedbackRequest.RequestID == "" || feedbackRequest.RequestID == setRequest.RequestID {
		t.Fatalf("classifier_feedback sent the wrong frame: %+v", feedbackRequest)
	}
}

func decodeRoleTool(t *testing.T, content any, out any) {
	t.Helper()
	data, err := json.Marshal(content)
	requireNoError(t, err)
	requireNoError(t, json.Unmarshal(data, out))
}
