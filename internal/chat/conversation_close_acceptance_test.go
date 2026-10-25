package chat_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Read the displayed bar up to its divider, allowing the new multi-row bar.
func closeConversationBar(t *testing.T, h *chatHarness) string {
	t.Helper()
	rows := strings.Split(ansi.Strip(h.m.View().Content), "\n")
	var bar []string
	for _, row := range rows[1:] {
		if strings.Trim(row, "─") == "" {
			return strings.Join(bar, "\n")
		}
		bar = append(bar, row)
	}
	t.Fatal("conversation bar divider missing")
	return ""
}

func TestConversationCloseAcceptancePreservesDraftHistoryAndAcknowledgment(t *testing.T) {
	h := conversationHarness(t, "alice", "bob")
	conversationOpen(h, "alice")
	v2Type(h, "unfinished private draft")
	before := h.m.State()
	v2Command(h, "/close")
	chatEqual(t, "closing selected conversation returns to All", h.m.State().SelectedAll, true)
	chatEqual(t, "closing preserves stored history", len(h.m.State().Entries), len(before.Entries))
	chatEqual(t, "closing queues no acknowledgment", len(h.m.State().QueuedAcks), 0)
	chatEqual(t, "closing makes no network request", h.m.State().InFlight, 0)
	chatEqual(t, "closing does not dispatch a draft", h.m.State().Intent, nil)
	audienceAckState(t, h, "conversation-target", chat.AckNeeded)
	if strings.Contains(closeConversationBar(t, h), "alice") {
		t.Fatal("hidden view remains in visible bar")
	}
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
		Kind: protocol.EventPresenceChanged, AgentID: "alice", SessionID: "new-alice-session", State: "connected",
		Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2},
	}}
	h.await(func() bool { return h.m.State().Revision == 2 })
	if strings.Contains(closeConversationBar(t, h), "alice") {
		t.Fatal("presence change reopened hidden conversation")
	}
	conversationOpen(h, "alice")
	chatEqual(t, "explicit reopen restores draft", h.m.State().Draft, "unfinished private draft")
	v3Contains(t, closeConversationBar(t, h), "alice")
	v3Contains(t, h.m.State().Hint, "to alice")
	v3Contains(t, ansi.Strip(h.m.View().Content), "body conversation-target")
}

func TestConversationCloseAcceptanceAllCannotBeHidden(t *testing.T) {
	h := conversationHarness(t, "alice")
	v2Type(h, "public draft")
	v2Command(h, "/close")
	chatEqual(t, "All remains selected", h.m.State().SelectedAll, true)
	chatEqual(t, "All draft remains", h.m.State().Draft, "public draft")
	v3Contains(t, closeConversationBar(t, h), "All")
	chatEqual(t, "no close network effects", h.m.State().InFlight, 0)
}

func TestConversationCloseAcceptanceNewMessageReopensWithoutSelectingOrAcking(t *testing.T) {
	h := conversationHarness(t, "alice", "bob")
	conversationOpen(h, "alice")
	v2Command(h, "/close")
	conversationOpen(h, "bob")
	if strings.Contains(closeConversationBar(t, h), "alice") {
		t.Fatal("precondition: alice should be hidden")
	}
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventConversationChanged, Direction: "received", Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	request := h.take(protocol.TypeOperatorReceive)
	msg := chatIncoming("reopen-arrival", 8)
	msg.From = "alice"
	h.reply(request, chatInbox(msg))
	receipt := h.take(protocol.TypeOperatorReceived)
	h.reply(receipt, chatReceipt(msg.MessageID))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, closeConversationBar(t, h), "alice")
	chatEqual(t, "new traffic does not steal selection", h.m.State().Selected, "bob")
	chatEqual(t, "reopened view has unread arrival", conversationBadge(t, h, "alice", false), 1)
	audienceAckState(t, h, msg.MessageID, chat.AckNeeded)
	chatEqual(t, "reopening does not queue acknowledgment", len(h.m.State().QueuedAcks), 0)
}
