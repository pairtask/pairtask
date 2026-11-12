//go:build linux

package main

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/bridge"
	"github.com/pairtask/pairtask/internal/desktop"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestDesktopMCPAcceptanceSharedAgents(t *testing.T) {
	f := newDesktopMCPFixture(t)
	f.redeem(t)
	worker := f.b.connectAgent(t, "b-worker")
	private := f.b.connectAgent(t, "b-private")
	f.b.shareAgents(t, f.identity, "b-worker")
	t.Run("fresh_directory_and_message", func(t *testing.T) { f.expectFreshInbox(t) })
	t.Run("send_and_reply", func(t *testing.T) {
		question := f.sendToAgent(t, "b-worker", "question for B's worker")
		got := awaitAgentMessage(t, worker, "question for B's worker")
		if got.Origin != protocol.OriginRemote {
			t.Fatalf("B's worker received origin %q", got.Origin)
		}
		if got.From != protocol.OperatorID {
			t.Fatalf("B's worker received sender %q", got.From)
		}
		if got.Remote == nil {
			t.Fatal("B's worker received no remote provenance")
		}
		if got.Remote.Contact != f.identity {
			t.Fatalf("B's worker received contact %q", got.Remote.Contact)
		}
		f.replyFromAgent(t, worker, got.MessageID, question.ID)
	})
	t.Run("agent_initiates", func(t *testing.T) { f.agentStartsConversation(t, worker) })
	t.Run("unshared_refusal", func(t *testing.T) { f.refuseUnsharedAgent(t, private) })
}

func TestDesktopMCPAcceptanceInboxReportsOfflinePeer(t *testing.T) {
	f := newDesktopMCPFixture(t)
	f.redeem(t)
	f.b.stop()
	inbox := f.inbox(t)
	if len(inbox.Conversations) != 1 {
		t.Fatalf("expected one paired workspace after B stopped: %+v", inbox.Conversations)
	}
	conv := inbox.Conversations[0]
	if conv.Fresh {
		t.Fatalf("offline inbox claimed to be fresh: %+v", conv)
	}
	if conv.SyncFailure == "" {
		t.Fatalf("offline inbox omitted the sync failure: %+v", conv)
	}
}

func (f *fullPeerFixture) connectAgent(t *testing.T, id string) *bridge.Client {
	t.Helper()
	agent, err := bridge.Dial(context.Background(), bridge.ClientConfig{SocketPath: f.agentSocket, AgentID: id, Harness: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Close)
	return agent
}

func (f *fullPeerFixture) shareAgents(t *testing.T, contact string, agents ...string) {
	t.Helper()
	resp := f.call(t, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpShare,
		ContactKey: contact, ContactAgents: agents})
	if resp.Type != protocol.TypeOperatorContactResult {
		t.Fatalf("share returned %s", resp.Type)
	}
	if resp.ContactPage == nil {
		t.Fatal("share returned no contact page")
	}
	if !slices.Equal(resp.ContactPage.Shared, agents) {
		t.Fatalf("B shared %+v, want %+v", resp.ContactPage.Shared, agents)
	}
}

func (f *desktopMCPFixture) expectFreshInbox(t *testing.T) {
	t.Helper()
	f.b.call(t, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend,
		ContactKey: f.identity, ContactText: "B's update while Desktop is idle"})
	inbox := f.inbox(t) // one user-visible check must pull from the dial-only Desktop
	if len(inbox.Conversations) != 1 {
		t.Fatalf("check_inbox returned %+v", inbox.Conversations)
	}
	conv := inbox.Conversations[0]
	if !conv.Fresh {
		t.Fatalf("check_inbox returned stale data: %+v", conv)
	}
	if !slices.Contains(conv.Directory, "b-worker") {
		t.Fatalf("check_inbox missed B's directory: %+v", conv)
	}
	if !containsContactText(conv.Messages, "B's update while Desktop is idle") {
		t.Fatalf("check_inbox missed B's message: %+v", conv)
	}
}

func containsContactText(messages []protocol.ContactMessageView, text string) bool {
	for _, message := range messages {
		if message.Direction == "in" && message.Text == text {
			return true
		}
	}
	return false
}

func (f *desktopMCPFixture) sendToAgent(t *testing.T, agent, message string) protocol.ContactMessageView {
	t.Helper()
	var sent desktop.SendResult
	decodeDesktopMCP(t, desktopTool(t, f.client, "send", map[string]any{
		"to": f.bIdentity, "agents": []string{agent}, "text": message,
	}), &sent)
	if sent.Message.ID == "" {
		t.Fatal("Desktop send returned no message ID")
	}
	if !slices.Equal(sent.Message.To, []string{agent}) {
		t.Fatalf("Desktop send did not target %s: %+v", agent, sent.Message)
	}
	return sent.Message
}

func awaitAgentMessage(t *testing.T, agent *bridge.Client, text string) protocol.Message {
	t.Helper()
	var found protocol.Message
	awaitDesktop(t, "B's agent receives Desktop message", func() bool {
		inbox, err := agent.Receive(context.Background(), 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range inbox.Messages {
			var payload struct {
				Text string `json:"text"`
			}
			if message.Type != protocol.TypeMsg {
				continue
			}
			if json.Unmarshal(message.Payload, &payload) != nil {
				continue
			}
			if payload.Text != text {
				continue
			}
			found = message
			return true
		}
		return false
	})
	return found
}

func (f *desktopMCPFixture) awaitMessage(t *testing.T, id string) protocol.ContactMessageView {
	t.Helper()
	var found protocol.ContactMessageView
	awaitDesktop(t, "Desktop inbox shows message "+id, func() bool {
		for _, message := range f.inbox(t).Conversations[0].Messages {
			if message.ID == id {
				found = message
				return true
			}
		}
		return false
	})
	return found
}

func (f *desktopMCPFixture) replyFromAgent(t *testing.T, agent *bridge.Client, importedID, originalID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reply, err := agent.SendMessage(ctx, bridge.SendRequest{To: protocol.OperatorID,
		InReplyTo: importedID, Text: "answer from B's worker"})
	if err != nil {
		t.Fatal(err)
	}
	view := f.awaitMessage(t, reply.MessageID)
	if view.Direction != "in" {
		t.Fatalf("agent reply direction: %+v", view)
	}
	if view.From != "b-worker" {
		t.Fatalf("agent reply sender: %+v", view)
	}
	if view.Text != "answer from B's worker" {
		t.Fatalf("agent reply text: %+v", view)
	}
	if view.ReplyID != originalID {
		t.Fatalf("agent reply ID: %+v", view)
	}
	if view.ReplySide != protocol.ReplyByReceiver {
		t.Fatalf("agent reply side: %+v", view)
	}
}

func (f *desktopMCPFixture) agentStartsConversation(t *testing.T, agent *bridge.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := agent.SendMessage(ctx, bridge.SendRequest{Workspace: f.identity, To: protocol.OperatorID,
		Text: "B's worker starts a conversation"})
	if err != nil {
		t.Fatal(err)
	}
	view := f.awaitMessage(t, out.MessageID)
	if view.Direction != "in" {
		t.Fatalf("agent-initiated message direction: %+v", view)
	}
	if view.From != "b-worker" {
		t.Fatalf("agent-initiated message sender: %+v", view)
	}
	if view.Text != "B's worker starts a conversation" {
		t.Fatalf("agent-initiated message text: %+v", view)
	}
}

func (f *desktopMCPFixture) refuseUnsharedAgent(t *testing.T, private *bridge.Client) {
	t.Helper()
	sent := f.sendToAgent(t, "b-private", "must not reach private agent")
	awaitDesktop(t, "unshared agent send is refused", func() bool {
		view := f.awaitMessage(t, sent.ID)
		return view.State == messaging.StateRefused
	})
	view := f.awaitMessage(t, sent.ID)
	if view.RefusalReason != messaging.ReasonNotShared {
		t.Fatalf("wrong unshared-agent refusal: %+v", view)
	}
	inbox, err := private.Receive(context.Background(), 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range inbox.Messages {
		if message.Type == protocol.TypeMsg && message.MessageID == sent.ID {
			t.Fatal("unshared agent received a refused message")
		}
	}
}
