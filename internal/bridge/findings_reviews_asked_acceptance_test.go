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

func TestFindingsReviewsAskedAcceptanceMCPDocumentsDetailAndPaging(t *testing.T) {
	c, _ := pipeClient(t)
	session := connectMCP(t, c)
	listed, err := session.ListTools(context.Background(), nil)
	requireNoError(t, err)
	for _, tool := range listed.Tools {
		if tool.Name != "reviews_asked" {
			continue
		}
		data, err := json.Marshal(tool)
		requireNoError(t, err)
		for _, word := range []string{"ask_id", "workspace", "before", "received", "their side"} {
			if !strings.Contains(string(data), word) {
				t.Fatalf("reviews_asked omits %q from its usable input schema/guidance: %s", word, data)
			}
		}
		return
	}
	t.Fatal("reviews_asked tool missing")
}

func TestFindingsReviewsAskedAcceptanceMCPRejectsIncompleteOrMixedSelectors(t *testing.T) {
	c, _ := pipeClient(t)
	c.protocolVersion = protocol.Version
	session := connectMCP(t, c)
	for _, args := range []map[string]any{{"ask_id": "ask"}, {"workspace": "sha256:" + strings.Repeat("a", 64)}, {"ask_id": "ask", "workspace": "sha256:" + strings.Repeat("a", 64), "before": "cursor"}} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "reviews_asked", Arguments: args})
		cancel()
		requireNoError(t, err)
		assertEqual(t, "invalid selector fails before writing to broker", result.IsError, true)
	}
}

// Exercise the real MCP handler and socket read loop, keeping the broker's
// request visible independently of the returned tool data.
func findingsAskedWire(remote net.Conn, answers []map[string]any, requests chan<- protocol.Message) error {
	defer close(requests)
	_ = remote.SetDeadline(time.Now().Add(4 * time.Second))
	r := bufio.NewReader(remote)
	for _, answer := range answers {
		var request protocol.Message
		if err := protocol.ReadFrame(r, &request); err != nil {
			return err
		}
		requests <- request
		answer["type"], answer["request_id"] = protocol.TypeReviewsAskedResult, request.RequestID
		if err := json.NewEncoder(remote).Encode(answer); err != nil {
			return err
		}
	}
	return nil
}

type findingsAskedPage struct {
	Asks []protocol.ReviewOutcomeView `json:"asks"`
	More bool                         `json:"more"`
	Next string                       `json:"next"`
}

func findingsAskedResult(t *testing.T, result *mcp.CallToolResult) findingsAskedPage {
	t.Helper()
	var out findingsAskedPage
	data, err := json.Marshal(result.StructuredContent)
	requireNoError(t, err)
	requireNoError(t, json.Unmarshal(data, &out))
	return out
}

func TestFindingsReviewsAskedAcceptanceMCPDetailUsesCanonicalIdentity(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = protocol.Version
	key := "sha256:" + strings.Repeat("a", 64)
	want := protocol.ReviewOutcomeView{AskID: "ask", Findings: &protocol.AskFindings{Total: 9, Received: 5, Items: []protocol.AskFinding{
		{Path: "one.go", Line: 1, Summary: "first finding"},
		{Path: "two.go", Line: 2, Summary: "second finding"},
		{Path: "three.go", Line: 3, Summary: "third finding"},
		{Path: "fourth.go", Line: 4, Summary: "fourth finding"},
		{Path: "five.go", Line: 5, Summary: "fifth finding"},
	}}}
	requests, done := make(chan protocol.Message, 1), make(chan error, 1)
	go func() {
		done <- findingsAskedWire(remote, []map[string]any{{"reviews_asked": []protocol.ReviewOutcomeView{want}}}, requests)
	}()
	session := connectMCP(t, c)
	got := findingsAskedResult(t, call(t, session, "reviews_asked", map[string]any{"ask_id": "ask", "workspace": key}))
	requireNoError(t, <-done)
	request := <-requests
	assertEqual(t, "detail uses correct read", request.Type, protocol.TypeReviewsAskedGet)
	assertEqual(t, "detail has exactly one identity", len(request.ReviewAskRefs), 1)
	assertEqual(t, "detail preserves canonical contact", request.ReviewAskRefs[0].Contact, key)
	assertEqual(t, "detail preserves ask id", request.ReviewAskRefs[0].AskID, "ask")
	assertEqual(t, "detail returns one ask", len(got.Asks), 1)
	if got.Asks[0].Findings == nil {
		t.Fatal("tool lost detail findings")
	}
	assertEqual(t, "tool retains all received items", len(got.Asks[0].Findings.Items), 5)
	assertEqual(t, "tool retains actual fourth finding", got.Asks[0].Findings.Items[3].Summary, "fourth finding")
}

func TestFindingsReviewsAskedAcceptanceMCPPassesEveryPageCursor(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = protocol.Version
	answers := []map[string]any{
		{"reviews_asked": []protocol.ReviewOutcomeView{{AskID: "newest"}}, "reviews_asked_more": true, "reviews_asked_next": "opaque-cursor"},
		{"reviews_asked": []protocol.ReviewOutcomeView{{AskID: "oldest"}}, "reviews_asked_more": false},
	}
	requests, done := make(chan protocol.Message, 2), make(chan error, 1)
	go func() { done <- findingsAskedWire(remote, answers, requests) }()
	session := connectMCP(t, c)
	first := findingsAskedResult(t, call(t, session, "reviews_asked", map[string]any{}))
	assertEqual(t, "tool exposes more pages", first.More, true)
	assertEqual(t, "tool exposes opaque continuation", first.Next, "opaque-cursor")
	last := findingsAskedResult(t, call(t, session, "reviews_asked", map[string]any{"before": first.Next}))
	requireNoError(t, <-done)
	assertEqual(t, "first page has no cursor", (<-requests).ReviewBefore, "")
	assertEqual(t, "continuation passes cursor unchanged", (<-requests).ReviewBefore, first.Next)
	assertEqual(t, "last page is terminal", last.More, false)
	seen := map[string]bool{}
	for _, page := range [][]protocol.ReviewOutcomeView{first.Asks, last.Asks} {
		for _, ask := range page {
			if seen[ask.AskID] {
				t.Fatalf("tool repeated ask %q", ask.AskID)
			}
			seen[ask.AskID] = true
		}
	}
	assertEqual(t, "all asks reachable", len(seen), 2)
}

func TestFindingsReviewsAskedAcceptanceOldBrokerDetailFailsBeforeWriting(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = protocol.ReviewFindingsVersion - 1
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.ReviewAskedDetail(ctx, protocol.ReviewAskRef{Contact: "sha256:" + strings.Repeat("a", 64), AskID: "ask"})
	assertErrorContains(t, err, "protocol version 21 required")
	requests, done := make(chan protocol.Message, 1), make(chan error, 1)
	go func() {
		done <- findingsAskedWire(remote, []map[string]any{{"reviews_asked": []protocol.ReviewOutcomeView{}}}, requests)
	}()
	_, err = c.ReviewsAsked(ctx)
	requireNoError(t, err)
	requireNoError(t, <-done)
	assertEqual(t, "only subsequent supported list reached broker", len((<-requests).ReviewAskRefs), 0)
}
