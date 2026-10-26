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

func TestRoomDeleteChatAcceptanceWaitsForSuccessKeepsDraftAndHistory(t *testing.T) {
	h := roomChatHarness(t)
	roomChatOpen(h, "alpha")
	v2Type(h, "unfinished room draft")
	v2Command(h, "/room delete alpha")
	req := h.take(protocol.TypeOperatorRoom)
	chatEqual(t, "delete request", req.RoomOp, "delete")
	chatEqual(t, "only requested room", req.Room, "alpha")
	v3Contains(t, h.m.State().Hint, "#alpha")
	chatEqual(t, "pending delete keeps draft", h.m.State().Draft, "unfinished room draft")
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorRoomResult, RoomList: &protocol.RoomList{Rooms: []protocol.RoomInfo{{ID: "beta"}}, Total: 1}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "successful delete returns to All", h.m.State().SelectedAll, true)
	if strings.Contains(closeConversationBar(t, h), "#alpha") {
		t.Fatal("deleted room remains in bar")
	}
	v3Contains(t, h.m.State().Status, "messages stay in All")
	v3Contains(t, h.m.State().Status, "draft is kept")
	v3Contains(t, ansi.Strip(h.m.View().Content), "body alpha-message")
	chatEqual(t, "delete does not acknowledge history", len(h.m.State().QueuedAcks), 0)
	v2Command(h, "/room alpha")
	req = h.take(protocol.TypeOperatorRoom)
	chatEqual(t, "reopen recreates room", req.RoomOp, protocol.RoomOpCreate)
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorRoomResult, RoomList: &protocol.RoomList{Rooms: []protocol.RoomInfo{{ID: "alpha"}, {ID: "beta"}}, Total: 2}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "draft restored on explicit reopen", h.m.State().Draft, "unfinished room draft")
}

func TestRoomDeleteChatAcceptanceRefusalDoesNotHideOrLoseDraft(t *testing.T) {
	h := roomChatHarness(t)
	roomChatOpen(h, "alpha")
	v2Type(h, "keep this")
	v2Command(h, "/room delete alpha")
	h.reply(h.take(protocol.TypeOperatorRoom), protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInvalidRoom, Detail: "no such room"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, h.m.State().Status, "no such room")
	v3Contains(t, closeConversationBar(t, h), "#alpha")
	chatEqual(t, "refusal retains draft", h.m.State().Draft, "keep this")
}

func TestRoomDeleteChatAcceptanceCompletesDeleteRoomAndRejectsMalformedCommands(t *testing.T) {
	h := roomChatHarness(t)
	h.input(chatKey('k', tea.ModCtrl))
	v2Type(h, "room delete al")
	commandCompleteItems(t, h, []string{"alpha"})
	h.input(chatKey(tea.KeyTab, 0))
	chatEqual(t, "delete room completes", h.m.State().CommandLine.Text, "room delete alpha ")
	chatEqual(t, "completion never deletes", h.m.State().InFlight, 0)
	h.input(chatKey(tea.KeyEscape, 0))
	for _, command := range []string{"/room delete", "/room delete alpha extra", "/room delete square"} {
		v2Command(h, command)
		chatEqual(t, "invalid delete sends nothing", h.m.State().InFlight, 0)
		h.input(chatKey(tea.KeyEscape, 0))
	}
}

func forgottenGroupArrival(h *chatHarness) {
	h.t.Helper()
	msg := chatIncoming("forgotten-group-arrival", 5)
	msg.From, msg.To, msg.Origin = protocol.OperatorID, "group:new-opaque", protocol.OriginOperator
	msg.Group = []string{"beta", "alpha"}
	page := chatHistory(msg)
	page.History.Entries[0].Direction = "sent"
	page.History.Watermark.Revision = 2
	h.event(protocol.EventConversationChanged, "epoch", 2)
	h.reply(h.take(protocol.TypeOperatorHistory), page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func TestForgetChatAcceptanceGroupStaysHiddenOnTrafficUntilExplicitReopen(t *testing.T) {
	h := groupConversationHarness(t)
	v2Command(h, "/chat @alpha @beta")
	v2Type(h, "saved group draft")
	v2Command(h, "/forget")
	chatEqual(t, "forget returns to All", h.m.State().SelectedAll, true)
	chatEqual(t, "group hidden", h.m.State().HiddenViews, 1)
	v3Contains(t, h.m.State().Status, "messages stay in All")
	v3Contains(t, h.m.State().Status, "draft is kept")
	forgottenGroupArrival(h)
	chatEqual(t, "new group traffic stays hidden", h.m.State().HiddenViews, 1)
	v3Contains(t, ansi.Strip(h.m.View().Content), "body forgotten-group-arrival")
	chatEqual(t, "forget does not acknowledge", len(h.m.State().QueuedAcks), 0)
	v2Command(h, `/chat ["beta","alpha"]`)
	chatEqual(t, "explicit same-member reopen", h.m.State().HiddenViews, 0)
	chatEqual(t, "group draft retained", h.m.State().Draft, "saved group draft")
	chatEqual(t, "same group audience", slices.Equal(h.m.State().Audience.IDs, []string{"alpha", "beta"}), true)
	v2Command(h, "/close")
	// Forgetting a group must not change unrelated tab state.
	conversationOpen(h, "group:foo")
	chatEqual(t, "literal group-like ID distinct", h.m.State().Selected, "group:foo")
}

func TestForgetChatAcceptanceDirectTrafficStaysHiddenAndExplicitReopenKeepsDraft(t *testing.T) {
	h := conversationHarness(t, "alice")
	conversationOpen(h, "alice")
	v2Type(h, "private draft")
	v2Command(h, "/forget")
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventConversationChanged, Direction: "received", Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	msg := chatIncoming("forgotten-direct-arrival", 8)
	msg.From = "alice"
	h.reply(h.take(protocol.TypeOperatorReceive), chatInbox(msg))
	h.reply(h.take(protocol.TypeOperatorReceived), chatReceipt(msg.MessageID))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	if strings.Contains(closeConversationBar(t, h), "alice") {
		t.Fatal("incoming traffic reopened forgotten direct tab")
	}
	v3Contains(t, ansi.Strip(h.m.View().Content), "body forgotten-direct-arrival")
	audienceAckState(t, h, msg.MessageID, chat.AckNeeded)
	conversationOpen(h, "alice")
	chatEqual(t, "private draft restored", h.m.State().Draft, "private draft")
}

func TestForgetChatAcceptanceRefusesAllAndRoomsAndExplainsLiteralIDReopen(t *testing.T) {
	h := roomChatHarness(t)
	v2Type(h, "All draft")
	v2Command(h, "/forget")
	chatEqual(t, "All stays selected", h.m.State().SelectedAll, true)
	chatEqual(t, "All draft preserved", h.m.State().Draft, "All draft")
	roomChatOpen(h, "alpha")
	v2Command(h, "/forget")
	v3Contains(t, h.m.State().Status, "/room delete")
	v3Contains(t, closeConversationBar(t, h), "#alpha")
	literal := conversationHarness(t, "worker with spaces")
	conversationOpen(literal, "worker with spaces")
	chatEqual(t, "literal agent selected before forget", literal.m.State().Selected, "worker with spaces")
	v2Command(literal, "/forget")
	v3Contains(t, literal.m.State().Status, `/chat "worker with spaces"`)
}

func TestForgetChatAcceptanceBoundDoesNotEvictEarlierSuppression(t *testing.T) {
	h := conversationHarness(t)
	for i := 0; i < 64; i++ {
		v2Command(h, fmt.Sprintf("/chat @member-%02d @peer", i))
		v2Command(h, "/forget")
		chatEqual(t, "forgotten conversation returns to All", h.m.State().SelectedAll, true)
	}
	v2Command(h, "/chat @extra @peer")
	v2Command(h, "/forget")
	v3Contains(t, h.m.State().Status, "64")
	chatEqual(t, "capacity refusal keeps selected conversation", h.m.State().SelectedAll, false)
	// The original view has left the loaded directory, but its suppression
	// must still occupy its slot until an explicit reopen clears it.
	v2Command(h, "/chat @member-00 @peer")
	v2Command(h, "/chat @extra @peer")
	v2Command(h, "/forget")
	chatEqual(t, "explicit reopen frees one suppression slot", h.m.State().SelectedAll, true)
}
