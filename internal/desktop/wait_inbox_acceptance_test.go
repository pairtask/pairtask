package desktop

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type inboxWaitView struct {
	Ready    bool     `json:"ready"`
	Contacts []string `json:"contacts"`
}

func inboxWaitAnswer(t *testing.T, result *mcp.CallToolResult, err error) inboxWaitView {
	t.Helper()
	incrementalOK(t, err)
	if result == nil || result.IsError {
		t.Fatalf("wait_inbox failed: %+v", result)
	}
	data, err := json.Marshal(result.StructuredContent)
	incrementalOK(t, err)
	var answer inboxWaitView
	incrementalOK(t, json.Unmarshal(data, &answer))
	return answer
}

func inboxWaitParams(contact string, seconds int) *mcp.CallToolParams {
	return &mcp.CallToolParams{Name: "wait_inbox", Arguments: map[string]any{
		"from": contact, "consumer": "waiter", "timeout_seconds": seconds}}
}

func inboxWaitReady(t *testing.T, answer inboxWaitView, contact string) {
	t.Helper()
	if !answer.Ready {
		t.Fatalf("incoming message not ready: %+v", answer)
	}
	if !reflect.DeepEqual(answer.Contacts, []string{contact}) {
		t.Fatalf("ready contacts = %v, want only %s", answer.Contacts, contact)
	}
}

func TestWaitInboxAcceptanceExistingMessageDoesNotAdvanceSeen(t *testing.T) {
	f := newIncrementalFixture(t)
	f.incoming(t, 1)
	client := desktopSpaceMCP(t, f.node)
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		result, err := client.CallTool(ctx, inboxWaitParams(f.contact, 1))
		cancel()
		answer := inboxWaitAnswer(t, result, err)
		inboxWaitReady(t, answer, f.contact)
	}
	reads, err := f.node.own.st.PeerMessages().PendingReads(context.Background(), f.contact, 50)
	incrementalOK(t, err)
	if len(reads) != 0 {
		t.Fatalf("waiting manufactured read receipts: %v", reads)
	}
	incrementalIDs(t, incrementalRead(t, f.node, map[string]any{"new_only": true, "consumer": "waiter"}), "in-1")
}

func TestWaitInboxAcceptanceTimeoutCapsPollIntervalAndIgnoresOutgoing(t *testing.T) {
	f := newIncrementalFixture(t)
	f.outgoing(t, 1)
	client := desktopSpaceMCP(t, f.node)
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, err := client.CallTool(ctx, inboxWaitParams(f.contact, 1))
	elapsed := time.Since(started)
	answer := inboxWaitAnswer(t, result, err)
	if answer.Ready || len(answer.Contacts) != 0 {
		t.Fatalf("outgoing message woke incoming wait: %+v", answer)
	}
	if elapsed < 800*time.Millisecond || elapsed >= 1800*time.Millisecond {
		t.Fatalf("one-second wait took %v", elapsed)
	}
}

type inboxWaitResult struct {
	result *mcp.CallToolResult
	err    error
}

func TestWaitInboxAcceptanceForwarderObservesMessageArrivingWhileWaiting(t *testing.T) {
	f := newIncrementalFixture(t)
	forwarder := incrementalStart(t, f.dir)
	client := desktopSpaceMCP(t, forwarder)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	answers := make(chan inboxWaitResult, 1)
	go func() {
		result, err := client.CallTool(ctx, inboxWaitParams(f.contact, 3))
		answers <- inboxWaitResult{result, err}
	}()
	select {
	case early := <-answers:
		t.Fatalf("empty inbox wait ended before a message arrived: %+v", early)
	case <-time.After(100 * time.Millisecond):
	}
	f.incoming(t, 1)
	select {
	case got := <-answers:
		answer := inboxWaitAnswer(t, got.result, got.err)
		inboxWaitReady(t, answer, f.contact)
	case <-ctx.Done():
		t.Fatal("forwarder wait did not wake within its timeout")
	}
}

func TestWaitInboxAcceptanceCancellationReturnsPromptly(t *testing.T) {
	f := newIncrementalFixture(t)
	client := desktopSpaceMCP(t, f.node)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	answers := make(chan error, 1)
	go func() {
		_, err := client.CallTool(ctx, inboxWaitParams(f.contact, 300))
		answers <- err
	}()
	select {
	case err := <-answers:
		t.Fatalf("long wait ended before cancellation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-answers:
		if err == nil {
			t.Fatal("cancelled call claimed successful wait completion")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled wait remained blocked")
	}
}
