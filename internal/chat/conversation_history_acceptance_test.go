package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func conversationOlderHarness(t *testing.T) *chatHarness {
	t.Helper()
	bounds := chat.DefaultBounds()
	bounds.CatchupPages = 2
	h := newChatHarness(t, bounds)
	h.input(tea.WindowSizeMsg{Width: 100, Height: 20})
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	message := chatIncoming("alice-current", 10)
	message.From = "alice"
	page := chatHistory(message)
	page.History.More = true
	h.reply(history, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	conversationOpen(h, "alice")
	return h
}

func conversationOlderPage(id, sender string, seq uint64, more bool) protocol.Message {
	message := chatIncoming(id, seq)
	message.From = sender
	page := chatHistory(message)
	page.History.UpperSeq, page.History.More = 10, more
	return page
}

func TestChatConversationOlderHistorySkipsUnrelatedPagesWithinBound(t *testing.T) {
	h := conversationOlderHarness(t)
	h.input(chatKey(tea.KeyPgUp, 0))
	first := h.take(protocol.TypeOperatorHistory)
	chatEqual(t, "first global cursor", first.PageRequest.BeforeSeq, uint64(10))
	h.reply(first, conversationOlderPage("bob-nine", "bob", 9, true))
	second := h.take(protocol.TypeOperatorHistory)
	chatEqual(t, "unrelated page advances global cursor", second.PageRequest.BeforeSeq, uint64(9))
	chatEqual(t, "history bound stays frozen", second.PageRequest.UpperSeq, uint64(10))
	h.reply(second, conversationOlderPage("bob-eight", "bob", 8, true))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, h.m.State().Status, "no older messages from alice in the last 2 pages; PgUp continues")
	h.input(chatKey(tea.KeyPgUp, 0))
	third := h.take(protocol.TypeOperatorHistory)
	chatEqual(t, "next key continues instead of restarting", third.PageRequest.BeforeSeq, uint64(8))
	h.reply(third, conversationOlderPage("alice-older", "alice", 7, false))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, ansi.Strip(h.m.View().Content), "body alice-older")
	if strings.Contains(ansi.Strip(h.m.View().Content), "body bob-") {
		t.Fatal("filtered history leaked unrelated messages")
	}
	v2Command(h, "/chat all")
	frame := ansi.Strip(h.m.View().Content)
	v3Contains(t, frame, "body bob-nine")
	v3Contains(t, frame, "body bob-eight")
}

func TestChatConversationOlderHistoryStopsAtFirstMatchingPage(t *testing.T) {
	h := conversationOlderHarness(t)
	h.input(chatKey(tea.KeyPgUp, 0))
	request := h.take(protocol.TypeOperatorHistory)
	h.reply(request, conversationOlderPage("alice-nine", "alice", 9, true))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, ansi.Strip(h.m.View().Content), "body alice-nine")
	chatEqual(t, "matching page ends the automatic run", h.m.State().Queued, 0)
}

func TestChatConversationLateStagesDoNotRevealBroadcastInAgentView(t *testing.T) {
	h := conversationHarness(t, "alice", "bob")
	broadcast := chatIncoming("older-broadcast", 11)
	broadcast.From, broadcast.To, broadcast.Origin = protocol.OperatorID, protocol.Broadcast, protocol.OriginOperator
	page := chatHistory(broadcast)
	page.History.Entries[0].Direction = "sent"
	page.History.Entries[0].Recipients = []protocol.RecipientStage{{AgentID: "alice", Stage: protocol.StageAccepted}}
	page.History.Entries[0].ReceiptsTruncated = true
	page.History.Watermark.Revision = 2
	h.event(protocol.EventConversationChanged, "epoch", 2)
	h.reply(h.take(protocol.TypeOperatorHistory), page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	_ = h.m.View()
	conversationOpen(h, "bob")
	if strings.Contains(ansi.Strip(h.m.View().Content), "body older-broadcast") {
		t.Fatal("presence guessed broadcast membership")
	}
	v2Command(h, "/stages #11")
	request := h.take(protocol.TypeOperatorMessageStages)
	h.reply(request, protocol.Message{Type: protocol.TypeOperatorMessageStages, Stages: &protocol.StagePage{
		Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}, MessageID: "older-broadcast",
		Recipients: []protocol.RecipientStage{{AgentID: "bob", Stage: protocol.StageAccepted}}, NextAfterAgent: "bob",
	}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyEscape, 0))
	if strings.Contains(ansi.Strip(h.m.View().Content), "body older-broadcast") {
		t.Fatal("receipt hydration put a public message in a direct conversation")
	}
	chatEqual(t, "late membership is not an arrival", h.m.State().Unread, 0)
	chatEqual(t, "late membership does not invent badge", conversationBadge(t, h, "bob", false), 0)
	v2Command(h, "/chat all")
	v3Contains(t, ansi.Strip(h.m.View().Content), "body older-broadcast")
}

func TestChatConversationOlderHistoryFailureResetsNextKeyPageBudget(t *testing.T) {
	h := conversationOlderHarness(t)
	h.input(chatKey(tea.KeyPgUp, 0))
	first := h.take(protocol.TypeOperatorHistory)
	h.reply(first, conversationOlderPage("bob-nine", "bob", 9, true))
	failed := h.take(protocol.TypeOperatorHistory)
	h.reply(failed, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "history unavailable"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyPgUp, 0))
	retry := h.take(protocol.TypeOperatorHistory)
	chatEqual(t, "retry starts at last successfully loaded cursor", retry.PageRequest.BeforeSeq, uint64(9))
	h.reply(retry, conversationOlderPage("bob-eight", "bob", 8, true))
	next := h.take(protocol.TypeOperatorHistory)
	chatEqual(t, "new key has a fresh two-page budget", next.PageRequest.BeforeSeq, uint64(8))
	h.reply(next, conversationOlderPage("alice-seven", "alice", 7, false))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, ansi.Strip(h.m.View().Content), "body alice-seven")
}
