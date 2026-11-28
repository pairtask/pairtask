package desktop_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pairtask/pairtask/internal/bridge"
	"github.com/pairtask/pairtask/internal/desktop"
	"github.com/pairtask/pairtask/internal/protocol"
)

// agentsStatus calls the node's agents_status tool.
func agentsStatus(t *testing.T, n *desktop.Node) map[string]protocol.AgentRead {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, b := mcp.NewInMemoryTransports()
	ss, err := desktop.NewServer(n, "test").Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "reads"}, nil).Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "agents_status", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("agents_status: %v %+v", err, res)
	}
	data, _ := json.Marshal(res.StructuredContent)
	var out desktop.AgentsStatusResult
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	byID := map[string]protocol.AgentRead{}
	for _, a := range out.Agents {
		byID[a.AgentID] = a
	}
	return byID
}

func TestEachAgentsReadStateIsItsOwn(t *testing.T) {
	dir := shortDir(t)
	n := ownerOf(t, dir)
	defer n.Close()
	claude, codex := macAgent(t, dir, "claude-desktop"), macAgent(t, dir, "codex")
	first := send(t, codex, bridge.SendRequest{To: "claude-desktop", Text: "one"})
	send(t, codex, bridge.SendRequest{To: "claude-desktop", Text: "two"})
	if got := delivered(claude, 2, 5*time.Second); got != 2 {
		t.Fatalf("Claude received %d of 2 messages", got)
	}

	before := agentsStatus(t, n)
	c, x := before["claude-desktop"], before["codex"]
	if c.Unread != 2 || c.OldestUnreadAt == nil || c.LastReadAt != nil || !c.Online {
		t.Fatalf("Claude before reading: %+v", c)
	}
	if x.Unread != 0 || x.OldestUnreadAt != nil || !x.Online {
		t.Fatalf("Codex, who was sent nothing: %+v", x)
	}

	if err := claude.Acknowledge(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	after := agentsStatus(t, n)["claude-desktop"]
	if after.Unread != 1 || after.LastReadAt == nil {
		t.Fatalf("Claude after acknowledging one: %+v", after)
	}
	if again := agentsStatus(t, n)["claude-desktop"]; again.Unread != 1 {
		t.Fatalf("asking twice changed the count: %+v", again)
	}
}

// delivered is how many of want peer messages reach c within wait.
func delivered(c *bridge.LazyClient, want int, wait time.Duration) int {
	got := 0
	deadline := time.Now().Add(wait)
	for got < want && time.Now().Before(deadline) {
		got += len(messages(c, time.Until(deadline)))
	}
	return got
}
