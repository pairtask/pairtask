package chat_test

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func conversationBadge(t *testing.T, h *chatHarness, id string, all bool) int {
	t.Helper()
	for _, view := range h.m.State().Conversations {
		if view.All == all && view.ID == id {
			return view.Unread
		}
	}
	t.Fatalf("missing conversation all=%v id=%q", all, id)
	return -1
}

func TestChatConversationKeyboardSwitchingAndPickerOwnership(t *testing.T) {
	h := conversationHarness(t, "alice", "bob")
	h.input(chatKey(tea.KeyTab, 0))
	chatEqual(t, "Tab selects first direct view", h.m.State().Selected, "alice")
	chatEqual(t, "direct view discriminant", h.m.State().SelectedAll, false)
	h.input(chatKey(tea.KeyTab, tea.ModShift))
	chatEqual(t, "Shift+Tab selects All", h.m.State().SelectedAll, true)
	h.input(chatKey('3', tea.ModAlt))
	chatEqual(t, "Alt+3 uses displayed position", h.m.State().Selected, "bob")
	v2Command(h, "/chat all")
	v2Type(h, "@ali")
	if h.m.State().Picker == nil {
		t.Fatal("test requires the mention picker")
	}
	h.input(chatKey(tea.KeyTab, 0))
	chatEqual(t, "picker Tab cannot change view", h.m.State().SelectedAll, true)
	v3Contains(t, h.m.State().Draft, "@alice")
	chatEqual(t, "navigation and completion never send", h.m.State().Intent, nil)
}

func TestChatConversationStateDistinguishesCommonAndLiteralAll(t *testing.T) {
	h := conversationHarness(t, "all")
	chatEqual(t, "common ID is empty", h.m.State().Selected, "")
	chatEqual(t, "common discriminant", h.m.State().SelectedAll, true)
	conversationOpen(h, "all")
	chatEqual(t, "literal ID preserved", h.m.State().Selected, "all")
	chatEqual(t, "literal all is direct", h.m.State().SelectedAll, false)
	common, direct := 0, 0
	for _, view := range h.m.State().Conversations {
		if view.All && view.ID == "" {
			common++
		}
		if !view.All && view.ID == "all" {
			direct++
		}
	}
	chatEqual(t, "one common view", common, 1)
	chatEqual(t, "one literal all view", direct, 1)
}

func TestChatConversationUnreadCountsHiddenArrivalsOnce(t *testing.T) {
	h := conversationHarness(t, "alice", "bob")
	_ = h.m.View()
	conversationOpen(h, "bob")
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
		Kind: protocol.EventConversationChanged, Direction: "received", Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2},
	}}
	request := h.take(protocol.TypeOperatorReceive)
	msg := chatIncoming("hidden-alice-arrival", 8)
	msg.From = "alice"
	h.reply(request, chatInbox(msg))
	receipt := h.take(protocol.TypeOperatorReceived)
	h.reply(receipt, chatReceipt(receipt.MessageIDs...))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	_ = h.m.View()
	chatEqual(t, "distinct unread total", h.m.State().Unread, 1)
	chatEqual(t, "All shares the entry flag", conversationBadge(t, h, "", true), 1)
	chatEqual(t, "Alice shares the entry flag", conversationBadge(t, h, "alice", false), 1)
	chatEqual(t, "Bob has no unrelated badge", conversationBadge(t, h, "bob", false), 0)
	conversationOpen(h, "alice")
	_ = h.m.View()
	chatEqual(t, "actually displaying entry clears total", h.m.State().Unread, 0)
	chatEqual(t, "displaying direct view clears All badge", conversationBadge(t, h, "", true), 0)
	audienceAckState(t, h, "hidden-alice-arrival", chat.AckNeeded)
}

func TestChatConversationSmallFramesKeepSelectedAudienceAndDraft(t *testing.T) {
	h := conversationHarness(t, "alice", "bob")
	conversationOpen(h, "alice")
	v2Type(h, "draft")
	for _, size := range [][2]int{{20, 4}, {32, 5}, {52, 8}, {80, 12}, {120, 24}} {
		h.input(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		v2FrameFits(t, h, size[0], size[1])
		v3Contains(t, h.m.State().Hint, "to alice")
		chatEqual(t, "resize preserves selected draft", h.m.State().Draft, "draft")
	}
}

func TestChatConversationHiddenTranscriptCannotClearUnread(t *testing.T) {
	for _, mode := range []string{"help", "too small"} {
		t.Run(mode, func(t *testing.T) {
			h := conversationHarness(t, "alice", "bob")
			_ = h.m.View()
			switch mode {
			case "help":
				h.input(chatKey(tea.KeyF1, 0))
			case "too small":
				h.input(tea.WindowSizeMsg{Width: 19, Height: 3})
			}
			h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
				Kind: protocol.EventConversationChanged, Direction: "received", Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2},
			}}
			request := h.take(protocol.TypeOperatorReceive)
			msg := chatIncoming("not-rendered", 8)
			msg.From = "alice"
			h.reply(request, chatInbox(msg))
			receipt := h.take(protocol.TypeOperatorReceived)
			h.reply(receipt, chatReceipt(receipt.MessageIDs...))
			h.await(func() bool { return h.m.State().InFlight == 0 })
			_ = h.m.View()
			chatEqual(t, "hidden transcript leaves arrival unread", h.m.State().Unread, 1)
			h.input(chatKey(tea.KeyEscape, 0))
			h.input(tea.WindowSizeMsg{Width: 120, Height: 30})
			_ = h.m.View()
			chatEqual(t, "showing transcript clears visible arrival", h.m.State().Unread, 0)
		})
	}
}
