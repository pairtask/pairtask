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

func TestContactHistoryBridgeAcceptanceSchemaExposesPairingSelector(t *testing.T) {
	c, _ := pipeClient(t)
	c.protocolVersion = 25
	session := connectMCP(t, c)
	listed, err := session.ListTools(context.Background(), nil)
	requireNoError(t, err)
	for _, tool := range listed.Tools {
		if tool.Name != "reviews_asked" {
			continue
		}
		data, err := json.Marshal(tool.InputSchema)
		requireNoError(t, err)
		if !strings.Contains(string(data), "pairing_epoch") {
			t.Fatal("reviews_asked does not expose listed pairing_epoch for detail selection")
		}
		if !strings.Contains(tool.Description, "pairing_epoch") {
			t.Fatal("reviews_asked guidance omits the listed epoch selector")
		}
		return
	}
	t.Fatal("reviews_asked tool missing")
}

func historyBridgeDetail(t *testing.T, epoch int64) protocol.Message {
	t.Helper()
	c, remote := pipeClient(t)
	c.protocolVersion = 25
	key := "sha256:" + strings.Repeat("a", 64)
	requests, done := make(chan protocol.Message, 1), make(chan error, 1)
	want := protocol.ReviewOutcomeView{AskID: "same-ask", Workspace: key, PairingEpoch: epoch, State: "completed"}
	go func() {
		done <- findingsAskedWire(remote, []map[string]any{{"reviews_asked": []protocol.ReviewOutcomeView{want}}}, requests)
	}()
	session := connectMCP(t, c)
	args := map[string]any{"ask_id": "same-ask", "workspace": key}
	if epoch != 0 {
		args["pairing_epoch"] = epoch
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "reviews_asked", Arguments: args})
	requireNoError(t, err)
	if result.IsError {
		t.Fatalf("valid history selector refused: %+v", result.Content)
	}
	out := findingsAskedResult(t, result)
	assertEqual(t, "one detail returned", len(out.Asks), 1)
	assertEqual(t, "result retains epoch", out.Asks[0].PairingEpoch, epoch)
	requireNoError(t, <-done)
	req := <-requests
	assertEqual(t, "one selected ask", len(req.ReviewAskRefs), 1)
	assertEqual(t, "canonical workspace", req.ReviewAskRefs[0].Contact, key)
	assertEqual(t, "wire ask ID", req.ReviewAskRefs[0].AskID, "same-ask")
	return req
}

func TestContactHistoryBridgeAcceptanceMCPForwardsListedEpoch(t *testing.T) {
	req := historyBridgeDetail(t, 1)
	assertEqual(t, "old pairing selected", req.ReviewAskRefs[0].PairingEpoch, int64(1))
}

func TestContactHistoryBridgeAcceptanceLegacyDetailKeepsCurrentDefault(t *testing.T) {
	req := historyBridgeDetail(t, 0)
	assertEqual(t, "legacy current default", req.ReviewAskRefs[0].PairingEpoch, int64(0))
}

func TestContactHistoryBridgeAcceptanceInvalidEpochSelectorsNeverReachBroker(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 25
	session := connectMCP(t, c)
	key := "sha256:" + strings.Repeat("a", 64)
	for _, args := range []map[string]any{
		{"pairing_epoch": 1},
		{"pairing_epoch": 1, "before": "cursor"},
		{"pairing_epoch": -1, "ask_id": "same-ask", "workspace": key},
		{"pairing_epoch": 1, "ask_id": "same-ask", "workspace": key, "before": "cursor"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "reviews_asked", Arguments: args})
		cancel()
		requireNoError(t, err)
		assertEqual(t, "invalid selector rejected", result.IsError, true)
	}
	// A timed-out broker write is not input rejection: no bytes may arrive.
	requireNoError(t, remote.SetReadDeadline(time.Now().Add(20*time.Millisecond)))
	var b [1]byte
	n, _ := remote.Read(b[:])
	assertEqual(t, "invalid selector emitted no broker bytes", n, 0)
}

func TestContactHistoryBridgeAcceptanceOldBrokerRefusesExplicitEpochBeforeWrite(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 24
	requests, done := make(chan protocol.Message, 1), make(chan error, 1)
	go func() {
		done <- findingsAskedWire(remote, []map[string]any{{"reviews_asked": []protocol.ReviewOutcomeView{}}}, requests)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.ReviewAskedDetail(ctx, protocol.ReviewAskRef{Contact: "sha256:" + strings.Repeat("a", 64), AskID: "same-ask", PairingEpoch: 1})
	if err == nil {
		t.Fatal("broker24 accepted explicit pairing_epoch and wrote a request")
	}
	if !strings.Contains(err.Error(), "25") {
		t.Fatalf("expected protocol25 compatibility refusal, got %v", err)
	}
	select {
	case req := <-requests:
		t.Fatalf("unsupported history selector reached old broker: %+v", req)
	default:
	}
}
