//go:build linux

package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/protocol"
)

var reviewToolNames = []string{"review_requests", "accept_review", "decline_review", "restart_review", "submit_review", "publish_review", "check_review"}

func reviewToolArgs(name string) map[string]any {
	args := map[string]any{"review_id": "review-7"}
	switch name {
	case "submit_review":
		args = reviewBridgeVerdict("approve")
		args["review_id"] = "review-7"
	case "restart_review":
		args["attempt"] = 1
	case "decline_review":
		args["reason"] = "outside expertise"
	}
	return args
}

type reviewObservedForge struct {
	forge.Forge
	calls atomic.Int32
}

func (f *reviewObservedForge) Identity(ctx context.Context) (string, error) {
	f.calls.Add(1)
	return f.Forge.Identity(ctx)
}
func (f *reviewObservedForge) Resolve(ctx context.Context, t forge.Target) (forge.Revisions, error) {
	f.calls.Add(1)
	return f.Forge.Resolve(ctx, t)
}
func (f *reviewObservedForge) Changes(ctx context.Context, t forge.Target, r forge.Revisions) ([]forge.FileChange, error) {
	f.calls.Add(1)
	return f.Forge.Changes(ctx, t, r)
}
func (f *reviewObservedForge) Publish(ctx context.Context, s forge.Submission) (forge.Receipt, error) {
	f.calls.Add(1)
	return f.Forge.Publish(ctx, s)
}
func (f *reviewObservedForge) Find(ctx context.Context, t forge.Target, e forge.Expected) (forge.Receipt, bool, error) {
	f.calls.Add(1)
	return f.Forge.Find(ctx, t, e)
}

func TestReviewBridgeAcceptanceDiscoveryIsLazy(t *testing.T) {
	path, _ := startBroker(t)
	session := connectMCP(t, lazyAgent(t, path, "alpha"))
	listed, err := session.ListTools(context.Background(), nil)
	requireNoError(t, err)
	names := claimToolNames(listed.Tools)
	for _, name := range reviewToolNames {
		assertEqual(t, "review tool discoverable: "+name, names[name], true)
	}
	assertEqual(t, "discovery never connects", *brokerStatus(t, path).ConnectedSessions, 0)
}

func TestReviewBridgeAcceptanceOldBrokerRefusesBeforeBrokerOrForgeIO(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 9
	f := &reviewObservedForge{Forge: forge.NewFake()}
	c.forges = forge.Registry{"fake": f}
	session := connectMCP(t, c)
	for _, name := range reviewToolNames {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: reviewToolArgs(name)})
		cancel()
		requireNoError(t, err)
		assertEqual(t, "old broker rejects "+name, r.IsError, true)
		body, err := json.Marshal(r.Content)
		requireNoError(t, err)
		assertEqual(t, "unsupported version explained", strings.Contains(string(body), "unsupported") && strings.Contains(string(body), "10"), true)
	}
	assertEqual(t, "unsupported tools make no forge calls", f.calls.Load(), int32(0))
	read := make(chan protocol.Message, 1)
	go func() {
		remote.SetReadDeadline(time.Now().Add(time.Second))
		var m protocol.Message
		_ = protocol.ReadFrame(bufio.NewReader(remote), &m)
		read <- m
	}()
	_, err := c.SendMessage(context.Background(), SendRequest{To: "peer", Text: "still usable", MessageID: "after-unsupported"})
	requireNoError(t, err)
	assertEqual(t, "no review frames were written", (<-read).MessageID, "after-unsupported")
}

func TestReviewBridgeAcceptanceWrongReviewerNeverTouchesForge(t *testing.T) {
	f := newReviewBridgeFixture(t)
	f.request(nil)
	f.accept()
	spy := &reviewObservedForge{Forge: f.forge}
	beta, err := Dial(context.Background(), ClientConfig{SocketPath: f.agent, AgentID: "beta", Forges: forge.Registry{"fake": spy}})
	requireNoError(t, err)
	t.Cleanup(beta.Close)
	session := connectMCP(t, beta)
	for _, name := range reviewToolNames {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: reviewToolArgs(name)})
		cancel()
		requireNoError(t, err)
		assertEqual(t, "wrong reviewer denied: "+name, r.IsError, true)
	}
	assertEqual(t, "authorization precedes forge I/O", spy.calls.Load(), int32(0))
	assertEqual(t, "task remains unchanged", f.view().State, "in_review")
}

func TestReviewBridgeAcceptanceSupersededAttemptRefusedBeforeDiffAccess(t *testing.T) {
	f := newReviewBridgeFixture(t)
	f.request(nil)
	f.accept()
	r := f.tool("restart_review", map[string]any{"attempt": 1})
	assertEqual(t, "restart succeeds", r.IsError, false)
	spy := &reviewObservedForge{Forge: f.forge}
	f.client.forges["fake"] = spy
	r = f.tool("submit_review", reviewBridgeVerdict("approve"))
	assertEqual(t, "old attempt refused", r.IsError, true)
	assertEqual(t, "old attempt performs no forge I/O", spy.calls.Load(), int32(0))
	assertEqual(t, "new attempt preserved", f.view().Attempt, 2)
}
