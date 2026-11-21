package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/protocol"
)

type freshnessForge struct {
	forge.Forge
	current forge.Revisions
	failure error
	calls   atomic.Int32
	bounded atomic.Bool
}

func (f *freshnessForge) Resolve(ctx context.Context, _ forge.Target) (forge.Revisions, error) {
	f.calls.Add(1)
	deadline, ok := ctx.Deadline()
	f.bounded.Store(ok && time.Until(deadline) <= 6*time.Second)
	return f.current, f.failure
}

func freshnessAsk() map[string]any {
	return map[string]any{"ask_id": "ask", "workspace": "sha256:" + strings.Repeat("a", 64), "forge": "fake", "repository": reviewBridgeTarget().Repository, "change": reviewBridgeTarget().Change, "state": "accepted", "requested": reviewBridgePins()}
}

func freshnessArgs(check bool) map[string]any {
	return map[string]any{"ask_id": "ask", "workspace": "sha256:" + strings.Repeat("a", 64), "check": check}
}

func freshnessWire(remote net.Conn, ask map[string]any, requests chan<- protocol.Message) error {
	defer close(requests)
	remote.SetDeadline(time.Now().Add(7 * time.Second))
	var request protocol.Message
	if err := protocol.ReadFrame(bufio.NewReader(remote), &request); err != nil {
		return err
	}
	requests <- request
	return json.NewEncoder(remote).Encode(map[string]any{"type": protocol.TypeReviewsAskedResult, "request_id": request.RequestID, "reviews_asked": []any{ask}})
}

func freshnessRead(t *testing.T, ask map[string]any, f *freshnessForge, args map[string]any) map[string]any {
	t.Helper()
	c, remote := pipeClient(t)
	c.protocolVersion = protocol.Version
	c.forges = forge.Registry{"fake": f}
	requests, done := make(chan protocol.Message, 1), make(chan error, 1)
	go func() { done <- freshnessWire(remote, ask, requests) }()
	session := connectMCP(t, c)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "reviews_asked", Arguments: args})
	requireNoError(t, err)
	if result.IsError {
		t.Fatalf("read failed: %+v", result.Content)
	}
	requireNoError(t, <-done)
	request := <-requests
	assertEqual(t, "only operation is read", request.Type, protocol.TypeReviewsAskedGet)
	encoded, err := json.Marshal(result.StructuredContent)
	requireNoError(t, err)
	var out struct {
		Asks []map[string]any `json:"asks"`
	}
	requireNoError(t, json.Unmarshal(encoded, &out))
	assertEqual(t, "one ask", len(out.Asks), 1)
	return out.Asks[0]
}

func freshnessField(t *testing.T, out map[string]any, state string) map[string]any {
	t.Helper()
	field, ok := out["freshness"].(map[string]any)
	if !ok {
		t.Fatalf("missing freshness: %+v", out)
	}
	assertEqual(t, "freshness state", field["state"], any(state))
	stamp, ok := field["checked_at"].(string)
	if !ok {
		t.Fatal("missing check time")
	}
	_, err := time.Parse(time.RFC3339Nano, stamp)
	requireNoError(t, err)
	return field
}

func TestReviewFreshnessAcceptanceCurrentAndMovedKeepOriginalPins(t *testing.T) {
	for _, change := range []string{"none", "head", "base"} {
		t.Run(change, func(t *testing.T) {
			current, state := freshnessScenario(change)
			f := &freshnessForge{Forge: forge.NewFake(), current: current}
			ask := freshnessAsk()
			before, _ := json.Marshal(ask)
			out := freshnessRead(t, ask, f, freshnessArgs(true))
			field := freshnessField(t, out, state)
			data, _ := json.Marshal(field["current"])
			var got protocol.Revisions
			requireNoError(t, json.Unmarshal(data, &got))
			assertEqual(t, "observed revisions", got, current)
			assertEqual(t, "one forge resolve", f.calls.Load(), int32(1))
			assertEqual(t, "bounded forge read", f.bounded.Load(), true)
			assertEqual(t, "lifecycle unchanged", out["state"], any("accepted"))
			requested, _ := json.Marshal(out["requested"])
			var pins protocol.Revisions
			requireNoError(t, json.Unmarshal(requested, &pins))
			assertEqual(t, "original pins", pins, reviewBridgePins())
			after, _ := json.Marshal(ask)
			assertEqual(t, "broker source unchanged", string(after), string(before))
		})
	}
}

func TestReviewFreshnessAcceptanceUnavailableIsUnknownNotAReadFailure(t *testing.T) {
	f := &freshnessForge{Forge: forge.NewFake(), failure: context.DeadlineExceeded}
	out := freshnessRead(t, freshnessAsk(), f, freshnessArgs(true))
	field := freshnessField(t, out, "unknown")
	if field["reason"] == nil || field["reason"] == "" {
		t.Fatal("unknown check lacks reason")
	}
	assertEqual(t, "state survives", out["state"], any("accepted"))
}

func TestReviewFreshnessAcceptanceOldBrokerPinsAreUnknown(t *testing.T) {
	ask := freshnessAsk()
	delete(ask, "requested")
	delete(ask, "forge")
	f := &freshnessForge{Forge: forge.NewFake(), current: reviewBridgePins()}
	freshnessField(t, freshnessRead(t, ask, f, freshnessArgs(true)), "unknown")
	assertEqual(t, "missing pins never guessed", f.calls.Load(), int32(0))
}

func TestReviewFreshnessAcceptanceListAndUncheckedDetailNeverResolve(t *testing.T) {
	for _, args := range []map[string]any{{}, freshnessArgs(false)} {
		f := &freshnessForge{Forge: forge.NewFake(), current: reviewBridgePins()}
		out := freshnessRead(t, freshnessAsk(), f, args)
		assertEqual(t, "no forge call", f.calls.Load(), int32(0))
		if _, exists := out["freshness"]; exists {
			t.Fatal("unchecked result claims freshness")
		}
		if out["requested"] == nil {
			t.Fatal("read omitted requested pins")
		}
		assertEqual(t, "forge retained", out["forge"], any("fake"))
	}
}

func freshnessScenario(change string) (protocol.Revisions, string) {
	current := reviewBridgePins()
	switch change {
	case "head":
		current.Head = strings.Repeat("3", 40)
	case "base":
		current.Base = strings.Repeat("3", 40)
	default:
		return current, "current"
	}
	return current, "moved"
}

func TestReviewFreshnessAcceptanceCheckNeedsOneCompleteReference(t *testing.T) {
	c, _ := pipeClient(t)
	c.protocolVersion = protocol.Version
	session := connectMCP(t, c)
	for _, args := range []map[string]any{{"check": true}, {"check": true, "ask_id": "ask"}, {"check": true, "workspace": "paired"}, {"check": true, "before": "cursor"}} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "reviews_asked", Arguments: args})
		cancel()
		requireNoError(t, err)
		if !result.IsError {
			t.Fatalf("incomplete checked selector accepted: %+v", args)
		}
	}
}
