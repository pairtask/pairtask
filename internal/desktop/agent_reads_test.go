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
	expectRead(t, "Claude before reading", before["claude-desktop"], readShape{online: true, unread: 2, oldest: true})
	expectRead(t, "Codex, who was sent nothing", before["codex"], readShape{online: true})

	if err := claude.Acknowledge(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	read := readShape{online: true, unread: 1, oldest: true, last: true}
	// Acknowledge writes to the agent socket. The operator status call can
	// arrive on its own connection before the broker commits the acknowledgment.
	eventually(t, "Claude’s acknowledgment appears in read state", func() bool {
		return shapeOf(agentsStatus(t, n)["claude-desktop"]) == read
	})
	expectRead(t, "Claude, asked again", agentsStatus(t, n)["claude-desktop"], read)
}

// readShape is what the test checks of an agent's reading: whether it is
// online, its unread count, and which of its times are set.
type readShape struct {
	online       bool
	unread       int
	oldest, last bool
}

func shapeOf(a protocol.AgentRead) readShape {
	return readShape{online: a.Online, unread: a.Unread, oldest: a.OldestUnreadAt != nil, last: a.LastReadAt != nil}
}

func expectRead(t *testing.T, what string, got protocol.AgentRead, want readShape) {
	t.Helper()
	if shapeOf(got) != want {
		t.Fatalf("%s: %+v, want %+v", what, got, want)
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
