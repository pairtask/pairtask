//go:build linux

package bridge

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/server"
	"github.com/pairtask/pairtask/internal/store"
)

type reviewBridgeFixture struct {
	t               *testing.T
	agent, operator string
	forge           *forge.Fake
	client          *Client
	mcp             *mcp.ClientSession
}

func reviewBridgeTarget() protocol.ReviewTarget {
	return protocol.ReviewTarget{Forge: "fake", Repository: "fake.example/space/repo", Change: "CR:7~revision"}
}

func reviewBridgePins() protocol.Revisions {
	return protocol.Revisions{Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40)}
}

func newReviewBridgeFixture(t *testing.T) *reviewBridgeFixture {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "review-mcp-")
	requireNoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "broker.db"))
	requireNoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := hub.New(st, 0, log)
	go h.Run(ctx)
	f := &reviewBridgeFixture{t: t, agent: filepath.Join(dir, "agent.sock"), operator: filepath.Join(dir, "operator.sock"), forge: forge.NewFake()}
	a, o := server.New(f.agent, h, log), server.NewOperator(f.operator, h, os.Getuid(), log)
	done := make(chan error, 2)
	go func() { done <- a.Listen(ctx) }()
	go func() { done <- o.Listen(ctx) }()
	t.Cleanup(func() {
		cancel()
		a.Close()
		o.Close()
		for n := 0; n < 2; n++ {
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(3 * time.Second):
				t.Error("review broker shutdown timed out")
			}
		}
		select {
		case <-h.Done():
		case <-time.After(3 * time.Second):
			t.Error("review hub shutdown timed out")
		}
		st.Close()
	})
	for _, path := range []string{f.agent, f.operator} {
		c := reviewBridgeDial(t, path)
		c.Close()
	}
	f.forge.SetRevisions(reviewBridgeTarget(), reviewBridgePins())
	f.forge.SetChanges(reviewBridgeTarget(), []forge.FileChange{{Path: "worker.go", Lines: []forge.LineRange{{From: 4, To: 7}}}})
	f.client, err = Dial(context.Background(), ClientConfig{SocketPath: f.agent, AgentID: "alpha", Forges: forge.Registry{"fake": f.forge}})
	requireNoError(t, err)
	t.Cleanup(f.client.Close)
	f.mcp = connectMCP(t, f.client)
	return f
}

func reviewBridgeDial(t *testing.T, path string) net.Conn {
	t.Helper()
	until := time.Now().Add(2 * time.Second)
	for {
		c, err := net.DialTimeout("unix", path, 100*time.Millisecond)
		if err == nil {
			return c
		}
		if time.Now().After(until) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
}

func (f *reviewBridgeFixture) operatorCall(m protocol.Message) protocol.Message {
	f.t.Helper()
	c := reviewBridgeDial(f.t, f.operator)
	defer c.Close()
	requireNoError(f.t, c.SetDeadline(time.Now().Add(2*time.Second)))
	requireNoError(f.t, protocol.WriteFrame(c, m))
	var r protocol.Message
	requireNoError(f.t, protocol.ReadFrame(bufio.NewReader(c), &r))
	if r.Type == "error" {
		f.t.Fatalf("operator review failed: %+v", r)
	}
	return r
}

func (f *reviewBridgeFixture) request(policy *protocol.ReviewPolicy) {
	f.t.Helper()
	f.operatorCall(protocol.Message{Type: "operator_review", ReviewOp: "request", MessageID: "review-7", ReviewSpec: &protocol.ReviewSpec{ReviewTarget: reviewBridgeTarget(), Reviewer: "alpha", Scope: "Check worker cancellation", Policy: policy}})
}

func (f *reviewBridgeFixture) view() protocol.ReviewView {
	f.t.Helper()
	r := f.operatorCall(protocol.Message{Type: "operator_review", ReviewOp: "status", ReviewID: "review-7"})
	if r.Review == nil {
		f.t.Fatal("operator status omitted review")
	}
	return *r.Review
}

func (f *reviewBridgeFixture) tool(name string, args map[string]any) *mcp.CallToolResult {
	f.t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	args["review_id"] = "review-7"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := f.mcp.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	requireNoError(f.t, err)
	return r
}

func (f *reviewBridgeFixture) accept() {
	f.t.Helper()
	r := f.tool("accept_review", nil)
	if r.IsError {
		f.t.Fatalf("accept_review: %+v", r.Content)
	}
	assertEqual(f.t, "review accepted", f.view().State, "in_review")
}

func reviewBridgeVerdict(outcome string) map[string]any {
	return map[string]any{"attempt": 1, "outcome": outcome, "summary": "Checked the pinned worker change", "findings": []protocol.Finding{{Path: "worker.go", Line: 5, Summary: "Check current attempt before save"}}}
}
