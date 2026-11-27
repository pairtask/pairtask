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
func messages(c *bridge.LazyClient, wait time.Duration) []protocol.Message {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if out := peerMessages(c); len(out) > 0 {
			return out
		}
	}
	return nil
}

// peerMessages are the peer messages one short receive brings; none after
// a reported gap, which the next call registers past.
func peerMessages(c *bridge.LazyClient) []protocol.Message {
	in, err := c.Receive(context.Background(), 100, 200*time.Millisecond)
	if err != nil {
		return nil
	}
	var out []protocol.Message
	for _, m := range in.Messages {
		if m.Type == protocol.TypeMsg {
			out = append(out, m)
		}
	}
	return out
}

// wantMsg is the one message expected: its sender, and its ID and the
// message it replies to when given.
type wantMsg struct{ from, id, parent string }

// only: msgs is exactly that message.
func (w wantMsg) only(msgs []protocol.Message) bool {
	if len(msgs) != 1 {
		return false
	}
	got := wantMsg{from: msgs[0].From, id: msgs[0].MessageID, parent: msgs[0].InReplyTo}
	if w.id == "" {
		got.id = ""
	}
	if w.parent == "" {
		got.parent = ""
	}
	return got == w
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
	got := messages(claude, 3*time.Second)
	if !(wantMsg{from: "codex", id: id}).only(got) {
		t.Fatalf("Claude got %+v, want codex's one message", got)
	}
	if extra := messages(codex, 500*time.Millisecond); len(extra) != 0 {
		t.Fatalf("a DM to Claude also reached Codex: %+v", extra)
	}

	send(t, claude, bridge.SendRequest{To: "codex", Text: "got it", InReplyTo: id})
	reply := messages(codex, 3*time.Second)
	if !(wantMsg{from: "claude-desktop", parent: id}).only(reply) {
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
	got := messages(claude, 5*time.Second)
	if !(wantMsg{from: "codex", id: id}).only(got) {
		t.Fatalf("after the restart Claude got %+v, want %s", got, id)
	}
	if extra := messages(codex, 500*time.Millisecond); len(extra) != 0 {
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
