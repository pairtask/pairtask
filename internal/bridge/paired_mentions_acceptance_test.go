package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

type pairedBridgeOutcome struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

func pairedBridgeWire(remote net.Conn, frames chan<- protocol.Message, outcome pairedBridgeOutcome) error {
	_ = remote.SetDeadline(time.Now().Add(4 * time.Second))
	var request protocol.Message
	if err := protocol.ReadFrame(bufio.NewReader(remote), &request); err != nil {
		return err
	}
	frames <- request
	// An unrelated accepted stage must not satisfy the reply's outcome watcher.
	for _, id := range []string{"unrelated-send", request.MessageID} {
		ack := map[string]any{"type": protocol.TypeAck, "stage": protocol.StageAccepted, "message_id": id,
			"peer_delivery": outcome}
		if err := json.NewEncoder(remote).Encode(ack); err != nil {
			return err
		}
	}
	return protocol.WriteFrame(remote, protocol.Message{Type: protocol.TypeMsg, MessageID: "unrelated-inbox", From: "peer"})
}

func pairedBridgeTransport(t *testing.T, outcome pairedBridgeOutcome) (*Client, <-chan protocol.Message, <-chan error) {
	t.Helper()
	c, remote := pipeClient(t)
	c.protocolVersion = 23
	frames, done := make(chan protocol.Message, 1), make(chan error, 1)
	go func() { done <- pairedBridgeWire(remote, frames, outcome) }()
	return c, frames, done
}

func pairedBridgeAssertResult(t *testing.T, result any, want pairedBridgeOutcome) {
	t.Helper()
	data, err := json.Marshal(result)
	requireNoError(t, err)
	var out struct {
		MessageID    string               `json:"message_id"`
		PeerDelivery *pairedBridgeOutcome `json:"peer_delivery"`
	}
	requireNoError(t, json.Unmarshal(data, &out))
	assertEqual(t, "stable message ID retained", out.MessageID, "home-answer")
	if out.PeerDelivery == nil {
		t.Fatal("tool result lost broker's structured peer delivery outcome")
	}
	assertEqual(t, "exact broker outcome retained", *out.PeerDelivery, want)
}

// Completing a net.Pipe write means the reader has the bytes, not that its
// decode/buffer step has finished. Wait on the inbox, bounded by the caller.
func pairedBridgeRecordID(t *testing.T, seen map[string]int, id string) {
	t.Helper()
	if _, expected := seen[id]; !expected {
		t.Fatalf("unexpected inbox frame %q", id)
	}
	seen[id]++
	if seen[id] != 1 {
		t.Fatalf("inbox duplicated frame %q", id)
	}
}

func pairedBridgeRecordBatch(t *testing.T, seen map[string]int, messages []protocol.Message) int {
	t.Helper()
	for _, message := range messages {
		pairedBridgeRecordID(t, seen, message.MessageID)
	}
	return len(messages)
}

func pairedBridgeReceivedIDs(t *testing.T, c *Client, ctx context.Context) map[string]int {
	t.Helper()
	seen := map[string]int{"home-answer": 0, "unrelated-send": 0, "unrelated-inbox": 0}
	remaining := len(seen)
	for remaining > 0 {
		inbox, err := c.Receive(ctx, 10, 50*time.Millisecond)
		if err != nil {
			t.Fatalf("inbox did not deliver every frame: seen=%v error=%v", seen, err)
		}
		remaining -= pairedBridgeRecordBatch(t, seen, inbox.Messages)
	}
	return seen
}

func TestPairedMentionsBridgeAcceptanceReplyOutcomeDoesNotConsumeAckOrInbox(t *testing.T) {
	for _, outcome := range []pairedBridgeOutcome{{State: "queued"}, {State: "not_sent", Reason: "not_shared"}, {State: "not_sent", Reason: "paused"}, {State: "not_sent", Reason: "contact_removed"}} {
		t.Run(outcome.State+"/"+outcome.Reason, func(t *testing.T) {
			c, frames, done := pairedBridgeTransport(t, outcome)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result, err := c.SendMessage(ctx, SendRequest{To: protocol.OperatorID, Text: "home answer", MessageID: "home-answer", InReplyTo: "paired-delivery"})
			requireNoError(t, err) // accepted local-only is a successful send, not an invitation to retry
			requireNoError(t, <-done)
			request := <-frames
			assertEqual(t, "canonical reply uses ordinary operator route", request.To, protocol.OperatorID)
			assertEqual(t, "binding resolved by broker", request.Workspace, "")
			assertEqual(t, "delivery alias preserved", request.InReplyTo, "paired-delivery")
			pairedBridgeAssertResult(t, result, outcome)
			seen := pairedBridgeReceivedIDs(t, c, ctx)
			for _, id := range []string{"home-answer", "unrelated-send", "unrelated-inbox"} {
				assertEqual(t, "normal inbox retains exactly one "+id, seen[id], 1)
			}
		})
	}
}

func TestPairedMentionsBridgeAcceptanceMCPReportsAcceptedLocalOnlyReply(t *testing.T) {
	want := pairedBridgeOutcome{State: "not_sent", Reason: "contact_removed"}
	c, _, done := pairedBridgeTransport(t, want)
	session := connectMCP(t, c)
	result := call(t, session, "send", map[string]any{"to": "operator", "text": "home answer", "message_id": "home-answer", "in_reply_to": "paired-root"})
	requireNoError(t, <-done)
	pairedBridgeAssertResult(t, result.StructuredContent, want)
}
