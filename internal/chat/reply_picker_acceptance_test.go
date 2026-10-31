package chat_test

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func replyPickerItems(t *testing.T, h *chatHarness, want ...string) {
	t.Helper()
	p := h.m.State().Picker
	if p == nil || p.Kind != "argument" || !reflect.DeepEqual(p.Items, want) {
		t.Fatalf("reply choices = %+v, want %v", p, want)
	}
}

func TestReplyPickerAcceptanceRoomAliasesOnlyFillThenBind(t *testing.T) {
	for _, command := range []string{"reply", "r"} {
		t.Run(command, func(t *testing.T) {
			h := roomChatHarness(t)
			roomChatOpen(h, "alpha")
			v2Type(h, "unfinished reply")
			v2Command(h, "/"+command)
			replyPickerItems(t, h, "#1")
			v3Contains(t, ansi.Strip(h.m.View().Content), "alpha-message")
			chatEqual(t, "opening never binds", h.m.State().InReplyTo, "")
			chatEqual(t, "opening never sends", h.m.State().InFlight, 0)
			h.input(chatKey(tea.KeyEnter, 0))
			chatEqual(t, "selection never binds", h.m.State().InReplyTo, "")
			chatEqual(t, "selection never sends", h.m.State().InFlight, 0)
			h.input(chatKey(tea.KeyEnter, 0))
			chatEqual(t, "bind exact room parent", h.m.State().InReplyTo, "alpha-message")
			chatEqual(t, "draft survives", h.m.State().Draft, "unfinished reply")
			chatEqual(t, "binding never acknowledges", len(h.m.State().QueuedAcks), 0)
			sent := v2Send(h)
			chatEqual(t, "room audience", sent.To, "*")
			chatEqual(t, "room scope", roomChatValue(t, sent), "alpha")
			chatEqual(t, "stable parent", sent.InReplyTo, "alpha-message")
		})
	}
}

func TestReplyPickerAcceptanceAllNewestFirstAndHistoryCannotRetarget(t *testing.T) {
	h := roomChatHarness(t)
	v2Command(h, "/reply")
	replyPickerItems(t, h, "#3", "#2", "#1")
	h.input(chatKey(tea.KeyDown, 0))
	h.input(chatKey(tea.KeyDown, 0))
	h.input(chatKey(tea.KeyEnter, 0))
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "picked parent", h.m.State().InReplyTo, "alpha-message")
	v2Type(h, "stable reply")
	h.event(protocol.EventConversationChanged, "epoch", 2)
	page := chatHistory(roomChatScope(t, publicChatMessage("newer-alpha", 4), "alpha"))
	page.History.Watermark.Revision = 2
	h.reply(h.take(protocol.TypeOperatorHistory), page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "history cannot retarget", h.m.State().InReplyTo, "alpha-message")
	roomChatOpen(h, "beta")
	roomChatOpen(h, "alpha")
	chatEqual(t, "switch restores same parent", h.m.State().InReplyTo, "alpha-message")
	sent := v2Send(h)
	chatEqual(t, "parent retained on send", sent.InReplyTo, "alpha-message")
	chatEqual(t, "scope retained on send", roomChatValue(t, sent), "alpha")
}

func TestReplyPickerAcceptanceEscapeAndCommandCancelKeepRoomDraft(t *testing.T) {
	for _, cancel := range []string{"escape", "command"} {
		t.Run(cancel, func(t *testing.T) {
			h := roomChatHarness(t)
			roomChatOpen(h, "alpha")
			v2Command(h, "/reply #1")
			v2Type(h, "keep these words")
			if cancel == "escape" {
				v3Contains(t, h.m.State().Hint, "Esc cancels")
				h.input(chatKey(tea.KeyEscape, 0))
			} else {
				v2Command(h, "/reply cancel")
			}
			chatEqual(t, "reply binding cancelled", h.m.State().InReplyTo, "")
			chatEqual(t, "text retained", h.m.State().Draft, "keep these words")
			chatEqual(t, "cancel sends nothing", h.m.State().InFlight, 0)
			chatEqual(t, "cancel acknowledges nothing", len(h.m.State().QueuedAcks), 0)
			sent := v2Send(h)
			chatEqual(t, "ordinary message after cancel", sent.InReplyTo, "")
			chatEqual(t, "same room after cancel", roomChatValue(t, sent), "alpha")
		})
	}
}

func TestReplyPickerAcceptanceExplicitOtherRoomKeepsParentScope(t *testing.T) {
	h := roomChatHarness(t)
	roomChatOpen(h, "alpha")
	v2Command(h, "/reply #2")
	v2Type(h, "answer beta")
	sent := v2Send(h)
	chatEqual(t, "explicit parent", sent.InReplyTo, "beta-message")
	chatEqual(t, "parent room, never Square", roomChatValue(t, sent), "beta")
	chatEqual(t, "public room audience", sent.To, "*")
}

func TestReplyPickerAcceptanceInvalidTargetPreservesDraft(t *testing.T) {
	h := roomChatHarness(t)
	roomChatOpen(h, "alpha")
	v2Type(h, "keep draft")
	v2Command(h, "/reply #999")
	chatEqual(t, "invalid target never binds", h.m.State().InReplyTo, "")
	chatEqual(t, "invalid target retains draft", h.m.State().Draft, "keep draft")
	chatEqual(t, "invalid target sends nothing", h.m.State().InFlight, 0)
	v3Contains(t, h.m.State().Status, "isn't loaded")
}

func TestReplyPickerAcceptanceDirectAndGroupFilterSenders(t *testing.T) {
	for _, conversation := range []string{"author", "@author @other"} {
		t.Run(conversation, func(t *testing.T) {
			first := chatIncoming("direct-parent", 1)
			other := chatIncoming("other-parent", 2)
			other.From = "other"
			outsider := chatIncoming("outsider", 3)
			outsider.From = "outsider"
			h := recallHarness(t,
				protocol.HistoryEntry{Message: outsider, Direction: "received", OperatorStage: protocol.StageAccepted},
				protocol.HistoryEntry{Message: other, Direction: "received", OperatorStage: protocol.StageAccepted},
				protocol.HistoryEntry{Message: first, Direction: "received", OperatorStage: protocol.StageAccepted})
			v2Command(h, "/chat "+conversation)
			v2Command(h, "/reply")
			if conversation == "author" {
				replyPickerItems(t, h, "#1")
			} else {
				replyPickerItems(t, h, "#2", "#1")
			}
			chatEqual(t, "filtering never sends", h.m.State().InFlight, 0)
			h.input(chatKey(tea.KeyEscape, 0))
			chatEqual(t, "cancel picker never binds", h.m.State().InReplyTo, "")
		})
	}
}

func TestReplyPickerAcceptanceRecallEscapePrecedesReplyCancel(t *testing.T) {
	parent := publicChatMessage("parent", 1)
	h := recallHarness(t, recallSent("old-text", 2, protocol.Broadcast), protocol.HistoryEntry{Message: parent, Direction: "received", OperatorStage: protocol.StageAccepted})
	h.input(chatKey(tea.KeyUp, 0))
	recallState(t, h, 0, 1)
	v2Command(h, "/reply #1")
	chatEqual(t, "reply bound during recall", h.m.State().InReplyTo, "parent")
	recallState(t, h, 0, 1)
	text := h.m.State().Draft
	h.input(chatKey(tea.KeyEscape, 0))
	recallEnded(t, h)
	chatEqual(t, "first Escape only ends recall", h.m.State().InReplyTo, "parent")
	h.input(chatKey(tea.KeyEscape, 0))
	chatEqual(t, "second Escape cancels reply", h.m.State().InReplyTo, "")
	chatEqual(t, "recalled text survives", h.m.State().Draft, text)
	chatEqual(t, "Escape sends nothing", h.m.State().InFlight, 0)
}
