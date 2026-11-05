package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/protocol"
)

func advisoryBridgeArgs() RequestReviewArgs {
	return RequestReviewArgs{Workspace: "sha256:" + strings.Repeat("a", 64), To: "reviewer", Target: "pairtask/pairtask#304", Base: strings.Repeat("1", 40), Head: strings.Repeat("2", 40), MessageID: "advisory-ask"}
}

type advisoryBridgeForge struct {
	forge.Forge
	started chan struct{}
	release chan struct{}
	err     error
	ctx     context.Context // published by closing started
}

func (f *advisoryBridgeForge) Resolve(ctx context.Context, _ forge.Target) (forge.Revisions, error) {
	f.ctx = ctx
	close(f.started)
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return forge.Revisions{}, ctx.Err()
		}
	}
	args := advisoryBridgeArgs()
	return forge.Revisions{Base: args.Base, Head: args.Head}, f.err
}

func (f *advisoryBridgeForge) CommitExists(context.Context, forge.Target, string) (bool, error) {
	return true, nil
}

func advisoryBridgeWire(remote net.Conn, frames chan<- protocol.Message) error {
	_ = remote.SetDeadline(time.Now().Add(7 * time.Second))
	var request protocol.Message
	if err := protocol.ReadFrame(bufio.NewReader(remote), &request); err != nil {
		return err
	}
	frames <- request
	// The advisory rides the real accepted stage; it must remain available
	// to the agent's usual receive/listener path after RequestReview returns.
	ack := map[string]any{"type": protocol.TypeAck, "stage": protocol.StageAccepted, "message_id": request.MessageID,
		"review_advisories": []map[string]any{{"code": "not_in_directory", "at": "2026-11-04T12:00:00Z"}}}
	if err := json.NewEncoder(remote).Encode(ack); err != nil {
		return err
	}
	return protocol.WriteFrame(remote, protocol.Message{Type: protocol.TypeMsg, MessageID: "unrelated-inbox", From: "peer"})
}

func advisoryBridgeTransport(t *testing.T) (*Client, <-chan protocol.Message, <-chan error) {
	t.Helper()
	c, remote := pipeClient(t)
	c.protocolVersion = protocol.Version
	frames, done := make(chan protocol.Message, 1), make(chan error, 1)
	go func() { done <- advisoryBridgeWire(remote, frames) }()
	return c, frames, done
}

type advisoryBridgeResult struct {
	Advisories []struct{ Code string }        `json:"advisories"`
	PinChecks  []struct{ Code, Class string } `json:"pin_checks"`
}

func advisoryBridgeReadResult(t *testing.T, result SendResult) advisoryBridgeResult {
	t.Helper()
	data, err := json.Marshal(result)
	requireNoError(t, err)
	var out advisoryBridgeResult
	requireNoError(t, json.Unmarshal(data, &out))
	return out
}

func advisoryBridgeRecordBatch(t *testing.T, seen map[string]int, messages []protocol.Message) int {
	t.Helper()
	for _, message := range messages {
		count, expected := seen[message.MessageID]
		if !expected || count != 0 {
			t.Fatalf("unexpected or duplicate inbox frame %q: seen=%v", message.MessageID, seen)
		}
		seen[message.MessageID] = 1
	}
	return len(messages)
}

func advisoryBridgeReceivedIDs(t *testing.T, c *Client, ctx context.Context) map[string]int {
	t.Helper()
	seen := map[string]int{advisoryBridgeArgs().MessageID: 0, "unrelated-inbox": 0}
	remaining := len(seen)
	// WriteFrame finishing only proves that the client read the bytes. Its
	// reader may still be scheduling the final frame onto the inbox.
	for remaining > 0 {
		inbox, err := c.Receive(ctx, 10, 50*time.Millisecond)
		requireNoError(t, err)
		remaining -= advisoryBridgeRecordBatch(t, seen, inbox.Messages)
	}
	return seen
}

func TestReviewAdvisoriesBridgeAcceptanceAgentPreflightFinishesBeforeQueue(t *testing.T) {
	c, frames, wireDone := advisoryBridgeTransport(t)
	f := &advisoryBridgeForge{Forge: forge.NewFake(), started: make(chan struct{}), release: make(chan struct{})}
	c.forges = forge.Registry{"github": f}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.RequestReview(ctx, advisoryBridgeArgs()); done <- err }()
	select {
	case <-f.started:
	case <-frames:
		t.Fatal("agent queued before its pin preflight ran")
	case <-ctx.Done():
		t.Fatal("agent never began pin preflight")
	}
	select {
	case <-frames:
		t.Fatal("agent queued while pin preflight was still blocked")
	case <-time.After(40 * time.Millisecond):
	}
	close(f.release)
	requireNoError(t, <-done)
	requireNoError(t, <-wireDone)
	request := <-frames
	if request.ReviewAsk == nil {
		t.Fatal("preflight lost original pinned review request")
	}
	assertEqual(t, "head never rewritten", request.ReviewAsk.Head, advisoryBridgeArgs().Head)
	assertEqual(t, "base never rewritten", request.ReviewAsk.Base, advisoryBridgeArgs().Base)
}

func TestReviewAdvisoriesBridgeAcceptanceAdvisoriesDoNotConsumeAckOrInbox(t *testing.T) {
	c, _, wireDone := advisoryBridgeTransport(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := c.RequestReview(ctx, advisoryBridgeArgs()) // no local adapter
	requireNoError(t, err)
	requireNoError(t, <-wireDone)
	out := advisoryBridgeReadResult(t, result)
	assertEqual(t, "broker advisory reaches tool result", len(out.Advisories), 1)
	assertEqual(t, "broker advisory code retained", out.Advisories[0].Code, "not_in_directory")
	assertEqual(t, "unsupported local check is explicit", len(out.PinChecks), 1)
	assertEqual(t, "unsupported check code", out.PinChecks[0].Code, "not_checked")
	assertEqual(t, "unsupported check class", out.PinChecks[0].Class, "unsupported")
	seen := advisoryBridgeReceivedIDs(t, c, ctx)
	assertEqual(t, "accepted stage remains on normal inbox path", seen[advisoryBridgeArgs().MessageID], 1)
	assertEqual(t, "unrelated message remains on normal inbox path", seen["unrelated-inbox"], 1)
}

func TestReviewAdvisoriesBridgeAcceptancePreflightTimeoutStillQueues(t *testing.T) {
	c, frames, wireDone := advisoryBridgeTransport(t)
	f := &advisoryBridgeForge{Forge: forge.NewFake(), started: make(chan struct{}), err: context.DeadlineExceeded}
	c.forges = forge.Registry{"github": f}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := c.RequestReview(ctx, advisoryBridgeArgs())
	requireNoError(t, err)
	requireNoError(t, <-wireDone)
	assertEqual(t, "timeout still sends original request", (<-frames).MessageID, advisoryBridgeArgs().MessageID)
	out := advisoryBridgeReadResult(t, result)
	assertEqual(t, "timeout is explicit", len(out.PinChecks), 1)
	assertEqual(t, "timeout is not a missing pin", out.PinChecks[0].Code, "not_checked")
	assertEqual(t, "timeout class", out.PinChecks[0].Class, "timeout")
}

func TestReviewAdvisoriesBridgeAcceptanceAgentPreflightIsBoundedToFiveSeconds(t *testing.T) {
	c, frames, wireDone := advisoryBridgeTransport(t)
	f := &advisoryBridgeForge{Forge: forge.NewFake(), started: make(chan struct{}), release: make(chan struct{})}
	c.forges = forge.Registry{"github": f}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	type answer struct {
		result SendResult
		err    error
	}
	done := make(chan answer, 1)
	start := time.Now()
	go func() {
		result, err := c.RequestReview(ctx, advisoryBridgeArgs())
		done <- answer{result, err}
	}()
	select {
	case <-f.started:
	case <-frames:
		t.Fatal("request bypassed the bounded preflight")
	case <-time.After(time.Second):
		t.Fatal("bounded preflight never started")
	}
	deadline, ok := f.ctx.Deadline()
	if !ok {
		t.Fatal("pin checker received no deadline")
	}
	if deadline.After(start.Add(5100 * time.Millisecond)) {
		t.Fatal("pin checker deadline exceeds five-second budget")
	}
	select {
	case got := <-done:
		requireNoError(t, got.err)
		out := advisoryBridgeReadResult(t, got.result)
		assertEqual(t, "elapsed preflight becomes one explicit check", len(out.PinChecks), 1)
		assertEqual(t, "elapsed preflight class", out.PinChecks[0].Class, "timeout")
	case <-time.After(6 * time.Second):
		t.Fatal("pin preflight prevented the valid request from queuing within its budget")
	}
	requireNoError(t, <-wireDone)
	assertEqual(t, "budget expiry still queues original request", (<-frames).MessageID, advisoryBridgeArgs().MessageID)
}
