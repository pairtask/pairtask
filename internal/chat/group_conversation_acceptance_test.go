package chat_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func groupConversationHarness(t *testing.T) *chatHarness {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 120, Height: 35})
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	var msgs []protocol.Message
	for i, members := range [][]string{{"beta", "alpha"}, {"alpha", "beta", "alpha"}, {"alpha", "gamma"}} {
		msg := chatIncoming(fmt.Sprintf("group-message-%d", i), uint64(i+1))
		msg.From, msg.Origin, msg.To = protocol.OperatorID, protocol.OriginOperator, fmt.Sprintf("group:opaque-%d", i)
		msg.Group = members
		msgs = append(msgs, msg)
	}
	literal := chatIncoming("literal-group-agent", 4)
	literal.From = "group:foo"
	msgs = append(msgs, literal)
	// History pages arrive newest first, with their cursor at the oldest entry.
	slices.Reverse(msgs)
	page := chatHistory(msgs...)
	for i := range page.History.Entries {
		if page.History.Entries[i].Message.Origin == protocol.OriginOperator {
			page.History.Entries[i].Direction = "sent"
		}
	}
	h.reply(history, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h
}

func TestGroupConversationAcceptanceCanonicalMembersAndLiteralAgent(t *testing.T) {
	h := groupConversationHarness(t)
	matching, literal := 0, 0
	for _, view := range h.m.State().Conversations {
		if slices.Equal(view.Group, []string{"alpha", "beta"}) {
			matching++
		}
		if view.ID == "group:foo" && len(view.Group) == 0 && !view.All {
			literal++
		}
		if strings.HasPrefix(view.ID, "group:opaque-") && len(view.Group) == 0 {
			t.Fatal("transport group ID became an agent conversation")
		}
	}
	chatEqual(t, "same member set has one conversation", matching, 1)
	chatEqual(t, "literal group-prefixed agent retained", literal, 1)
	v2Command(h, `/chat ["beta","alpha","alpha"]`)
	frame := ansi.Strip(h.m.View().Content)
	for _, body := range []string{"body group-message-0", "body group-message-1"} {
		v3Contains(t, frame, body)
	}
	if strings.Contains(frame, "body group-message-2") || strings.Contains(frame, "body literal-group-agent") {
		t.Fatal("group view includes another audience")
	}
	if strings.Contains(frame, "group:opaque-") {
		t.Fatal("opaque transport group ID leaked into display")
	}
	v2Type(h, "group draft")
	v2Command(h, "/chat all")
	v2Command(h, "/chat @alpha @beta")
	chatEqual(t, "alternate group syntax restores same draft", h.m.State().Draft, "group draft")
	req := v2Send(h)
	chatEqual(t, "group send uses canonical members", slices.Equal(req.Group, []string{"alpha", "beta"}), true)
	chatEqual(t, "group send does not become broadcast", req.To == protocol.Broadcast, false)
}

func TestGroupConversationAcceptanceHideReopenKeepsMembersAndHistory(t *testing.T) {
	h := groupConversationHarness(t)
	v2Command(h, "/chat @alpha @beta")
	v2Type(h, "saved group draft")
	v2Command(h, "/close")
	chatEqual(t, "group close returns to All", h.m.State().SelectedAll, true)
	chatEqual(t, "one hidden group", h.m.State().HiddenViews, 1)
	chatEqual(t, "hiding preserves all history", len(h.m.State().Entries), 4)
	chatEqual(t, "hiding makes no request", h.m.State().InFlight, 0)
	v2Command(h, `/chat ["beta","alpha"]`)
	chatEqual(t, "manual reopen clears hidden count", h.m.State().HiddenViews, 0)
	chatEqual(t, "group draft survives hide", h.m.State().Draft, "saved group draft")
	v3Contains(t, ansi.Strip(h.m.View().Content), "body group-message-0")
	v2Command(h, "/close")
	conversationOpen(h, "group:foo")
	chatEqual(t, "literal agent selected independently", h.m.State().Selected, "group:foo")
	v3Contains(t, ansi.Strip(h.m.View().Content), "body literal-group-agent")
	chatEqual(t, "literal agent does not reopen unrelated group", h.m.State().HiddenViews, 1)
}

func TestGroupConversationAcceptanceBarShowsOverflowAndFitsSmallFrames(t *testing.T) {
	var ids []string
	for i := 0; i < 18; i++ {
		ids = append(ids, fmt.Sprintf("worker-%02d", i))
	}
	h := conversationHarness(t, ids...)
	h.input(tea.WindowSizeMsg{Width: 52, Height: 24})
	bar := closeConversationBar(t, h)
	if len(strings.Split(bar, "\n")) > 3 {
		t.Fatal("bar exceeded three rows")
	}
	v3Contains(t, bar, "more")
	conversationOpen(h, "worker-17")
	v3Contains(t, closeConversationBar(t, h), "worker-17")
	v2Type(h, "draft remains")
	for _, size := range [][2]int{{20, 4}, {32, 5}, {52, 8}, {80, 24}} {
		h.input(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		v2FrameFits(t, h, size[0], size[1])
		chatEqual(t, "resize retains draft", h.m.State().Draft, "draft remains")
		chatEqual(t, "resize retains audience", h.m.State().Selected, "worker-17")
	}
	h.input(tea.WindowSizeMsg{Width: 52, Height: 24})
	v2Command(h, "/close")
	chatEqual(t, "one hidden view among overflowing tabs", h.m.State().HiddenViews, 1)
	bar = closeConversationBar(t, h)
	v3Contains(t, bar, "1 hidden")
	v3Contains(t, bar, "unread")
	v3Contains(t, bar, "more")
}

func TestGroupConversationAcceptanceOldHistoryDoesNotReopenButNewGroupMessageDoes(t *testing.T) {
	h := groupConversationHarness(t)
	v2Command(h, "/chat @alpha @beta")
	v2Command(h, "/close")
	conversationOpen(h, "group:foo")
	old := chatIncoming("group-message-0", 1)
	old.From, old.To, old.Origin = protocol.OperatorID, "group:opaque-0", protocol.OriginOperator
	old.Group = []string{"alpha", "beta"}
	page := chatHistory(old)
	page.History.Entries[0].Direction = "sent"
	page.History.UpperSeq = 4
	page.History.Watermark.Revision = 2
	h.event(protocol.EventConversationChanged, "epoch", 2)
	h.reply(h.take(protocol.TypeOperatorHistory), page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "history refresh does not reopen hidden group", h.m.State().HiddenViews, 1)
	newMessage := old
	newMessage.MessageID, newMessage.Seq, newMessage.To = "new-group-traffic", 5, "group:new-opaque-id"
	page = chatHistory(newMessage)
	page.History.Entries[0].Direction = "sent"
	page.History.Watermark.Revision = 3
	h.event(protocol.EventConversationChanged, "epoch", 3)
	h.reply(h.take(protocol.TypeOperatorHistory), page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "new message to same members reopens group", h.m.State().HiddenViews, 0)
	chatEqual(t, "new group traffic does not steal selection", h.m.State().Selected, "group:foo")
	chatEqual(t, "reopen does not acknowledge", len(h.m.State().QueuedAcks), 0)
}
