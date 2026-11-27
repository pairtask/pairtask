package desktop_test

import (
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/bridge"
	"github.com/pairtask/pairtask/internal/desktop"
	"github.com/pairtask/pairtask/internal/protocol"
)

func ownerOf(t *testing.T, dir string) *desktop.Node {
	t.Helper()
	n, err := desktop.Start(context.Background(), desktop.Config{Dir: dir, Name: "mac"})
	if err != nil {
		t.Fatal(err)
	}
	if !n.Owner() {
		t.Fatal("the first copy doesn't own the node")
	}
	return n
}

// macAgent is one of the Mac's apps registered as agent id.
func macAgent(t *testing.T, dir, id string) *bridge.LazyClient {
	t.Helper()
	c, err := bridge.NewRedialingClient(bridge.ClientConfig{SocketPath: desktop.AgentSocketPath(dir), AgentID: id, Harness: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	eventually(t, id+" registers", func() bool {
		in, err := c.Receive(context.Background(), 1, 0)
		return err == nil && in.Connected
	})
	return c
}

// messages are the peer messages in c's inbox within wait.
func messages(t *testing.T, c *bridge.LazyClient, wait time.Duration) []protocol.Message {
	t.Helper()
	var out []protocol.Message
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		in, err := c.Receive(context.Background(), 100, 200*time.Millisecond)
		if err != nil {
			continue // a reported gap; the next call registers again
		}
		for _, m := range in.Messages {
			if m.Type == protocol.TypeMsg {
				out = append(out, m)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return out
}

func send(t *testing.T, c *bridge.LazyClient, r bridge.SendRequest) string {
	t.Helper()
	out, err := c.SendMessage(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return out.MessageID
}

func TestEachMacAppIsItsOwnAgent(t *testing.T) {
	dir := shortDir(t)
	n := ownerOf(t, dir)
	defer n.Close()
	claude, codex := macAgent(t, dir, "claude-desktop"), macAgent(t, dir, "codex")

	id := send(t, codex, bridge.SendRequest{To: "claude-desktop", Text: "only for Claude"})
	got := messages(t, claude, 3*time.Second)
	if len(got) != 1 || got[0].From != "codex" || got[0].MessageID != id {
		t.Fatalf("Claude got %+v, want codex's one message", got)
	}
	if extra := messages(t, codex, 500*time.Millisecond); len(extra) != 0 {
		t.Fatalf("a DM to Claude also reached Codex: %+v", extra)
	}

	send(t, claude, bridge.SendRequest{To: "codex", Text: "got it", InReplyTo: id})
	reply := messages(t, codex, 3*time.Second)
	if len(reply) != 1 || reply[0].From != "claude-desktop" || reply[0].InReplyTo != id {
		t.Fatalf("Codex got %+v, want Claude's reply to %s", reply, id)
	}
}

func TestAMacAgentReconnectsAfterTheNodeMovesAndGetsItsUnreadMessages(t *testing.T) {
	dir := shortDir(t)
	first := ownerOf(t, dir)
	claude, codex := macAgent(t, dir, "claude-desktop"), macAgent(t, dir, "codex")
	id := send(t, codex, bridge.SendRequest{To: "claude-desktop", Text: "read me after the restart"})
	time.Sleep(300 * time.Millisecond) // accepted and stored before the node stops
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Claude is told its connection broke", func() bool {
		in, err := claude.Receive(context.Background(), 1, 0)
		return err != nil || (!in.Connected && in.Error != "")
	})

	second := ownerOf(t, dir)
	defer second.Close()
	// The first call after the gap reports it; a later one registers again
	// and gets the unacknowledged message replayed, to Claude only.
	got := messages(t, claude, 5*time.Second)
	if len(got) != 1 || got[0].MessageID != id {
		t.Fatalf("after the restart Claude got %+v, want %s", got, id)
	}
	if extra := messages(t, codex, 500*time.Millisecond); len(extra) != 0 {
		t.Fatalf("after the restart Codex got %+v", extra)
	}
}

func TestASecondSessionCantTakeAMacAgentsID(t *testing.T) {
	dir := shortDir(t)
	n := ownerOf(t, dir)
	defer n.Close()
	macAgent(t, dir, "codex")
	again, err := bridge.Dial(context.Background(), bridge.ClientConfig{SocketPath: desktop.AgentSocketPath(dir), AgentID: "codex", Harness: "test"})
	if err == nil {
		again.Close()
		t.Fatal("a second live session registered as codex")
	}
}
