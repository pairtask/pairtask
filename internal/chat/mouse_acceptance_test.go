package chat_test

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func mouseHarness(t *testing.T) *chatHarness {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 80, Height: 24})
	var messages []protocol.Message
	for i := 24; i >= 1; i-- {
		messages = append(messages, chatIncoming(fmt.Sprintf("wheel-%02d", i), uint64(i)))
	}
	v2History(h, messages...)
	return h
}

func TestChatMouseWheelUsesCellMotionAndThreeLineTranscriptSteps(t *testing.T) {
	h := mouseHarness(t)
	chatEqual(t, "cell-motion reporting enabled", h.m.View().MouseMode, tea.MouseModeCellMotion)
	for _, want := range []int{3, 6} {
		h.input(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelUp})
		chatEqual(t, "wheel up moves three transcript lines", h.m.State().ScrollOffset, want)
	}
	for _, want := range []int{3, 0} {
		h.input(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelDown})
		chatEqual(t, "wheel down returns toward following", h.m.State().ScrollOffset, want)
	}
	chatEqual(t, "wheel never submits", h.m.State().Intent, nil)
	chatEqual(t, "wheel never acknowledges", len(h.m.State().QueuedAcks), 0)
	v2FrameFits(t, h, 80, 24)
}

func TestChatMouseWheelAtTopLoadsOlderHistoryOnce(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 80, Height: 24})
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	page := chatHistory(chatIncoming("latest", 5))
	page.History.More = true
	h.reply(history, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	for range 5 {
		h.input(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelUp})
	}
	request := h.take(protocol.TypeOperatorHistory)
	chatEqual(t, "wheel uses older history cursor", request.PageRequest.BeforeSeq, uint64(5))
	chatEqual(t, "wheel keeps frozen upper bound", request.PageRequest.UpperSeq, uint64(5))
	chatEqual(t, "wheel coalesces loading", h.m.State().InFlight, 1)
	older := chatHistory(chatIncoming("older", 4))
	older.History.UpperSeq = 5
	h.reply(request, older)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, ansi.Strip(h.m.View().Content), "body older")
}

func TestChatMouseWheelOverOverflowingComposerDoesNotMoveTranscript(t *testing.T) {
	h := mouseHarness(t)
	draft := "row01\nrow02\nrow03\nrow04\nrow05\nrow06\nrow07\nrow08\nrow09"
	h.input(tea.PasteMsg{Content: draft})
	before := strings.Join(growthComposerRows(t, h), "\n")
	v3Contains(t, before, "row09")
	h.input(tea.MouseWheelMsg{X: 10, Y: mouseComposerY(h), Button: tea.MouseWheelUp})
	after := strings.Join(growthComposerRows(t, h), "\n")
	if after == before {
		t.Fatal("wheel did not scroll overflowing composer")
	}
	for range 3 {
		h.input(tea.MouseWheelMsg{X: 10, Y: mouseComposerY(h), Button: tea.MouseWheelUp})
	}
	v3Contains(t, strings.Join(growthComposerRows(t, h), "\n"), "row01")
	chatEqual(t, "composer wheel leaves transcript following", h.m.State().ScrollOffset, 0)
	chatEqual(t, "composer scroll leaves text intact", h.m.State().Draft, draft)
	for range 4 {
		h.input(tea.MouseWheelMsg{X: 10, Y: mouseComposerY(h), Button: tea.MouseWheelDown})
	}
	v3Contains(t, strings.Join(growthComposerRows(t, h), "\n"), "row09")
}

func TestChatMouseWheelInHelpLeavesDraftAndTranscriptUntouched(t *testing.T) {
	h := mouseHarness(t)
	v2Type(h, "unsent draft")
	h.input(chatKey(tea.KeyF1, 0))
	before := ansi.Strip(h.m.View().Content)
	h.input(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelDown})
	after := ansi.Strip(h.m.View().Content)
	if before == after {
		t.Fatal("wheel did not scroll the help panel")
	}
	chatEqual(t, "help wheel leaves transcript", h.m.State().ScrollOffset, 0)
	chatEqual(t, "help wheel preserves draft", h.m.State().Draft, "unsent draft")
	chatEqual(t, "help wheel sends nothing", h.m.State().Intent, nil)
}

func mouseFirstBody(t *testing.T, h *chatHarness) string {
	t.Helper()
	for _, row := range strings.Split(ansi.Strip(h.m.View().Content), "\n") {
		if strings.Contains(row, "body wheel-") {
			return strings.TrimSpace(row)
		}
	}
	t.Fatal("visible transcript contains no wheel fixture body")
	return ""
}

func TestChatMouseWheelAnchorSurvivesArrivalUntilFollowingResumes(t *testing.T) {
	h := mouseHarness(t)
	h.input(tea.WindowSizeMsg{Width: 80, Height: 200})
	_ = h.m.View() // Start with all historical messages already seen.
	h.input(tea.WindowSizeMsg{Width: 80, Height: 24})
	for range 3 {
		h.input(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelUp})
	}
	first := mouseFirstBody(t, h)
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
		Kind: protocol.EventConversationChanged, Direction: "received", Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2},
	}}
	request := h.take(protocol.TypeOperatorReceive)
	h.reply(request, chatInbox(chatIncoming("wheel-25", 25)))
	receipt := h.take(protocol.TypeOperatorReceived)
	h.reply(receipt, chatReceipt(receipt.MessageIDs...))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "arrival keeps the wheel anchor", mouseFirstBody(t, h), first)
	chatEqual(t, "arrival stays unread while scrolled", h.m.State().Unread, 1)
	for range 30 {
		h.input(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelDown})
	}
	_ = h.m.View()
	chatEqual(t, "bottom resumes following", h.m.State().ScrollOffset, 0)
	chatEqual(t, "showing arrival clears unread", h.m.State().Unread, 0)
	chatEqual(t, "scrolling never acknowledges", len(h.m.State().QueuedAcks), 0)
}

func TestChatMouseWheelOverSoftWrappedComposerDoesNotMoveTranscript(t *testing.T) {
	h := mouseHarness(t)
	draft := "START " + strings.Repeat("wrapped text ", 75) + "END"
	h.input(tea.PasteMsg{Content: draft})
	before := strings.Join(growthComposerRows(t, h), "\n")
	v3Contains(t, before, "END")
	h.input(tea.MouseWheelMsg{X: 10, Y: mouseComposerY(h), Button: tea.MouseWheelUp})
	chatEqual(t, "soft-wrapped composer owns the wheel", h.m.State().ScrollOffset, 0)
	if after := strings.Join(growthComposerRows(t, h), "\n"); after == before {
		t.Fatal("wheel did not scroll soft-wrapped composer")
	}
	for range 10 {
		h.input(tea.MouseWheelMsg{X: 10, Y: mouseComposerY(h), Button: tea.MouseWheelUp})
	}
	v3Contains(t, strings.Join(growthComposerRows(t, h), "\n"), "START")
	chatEqual(t, "soft-wrapped scroll preserves text", h.m.State().Draft, draft)
	chatEqual(t, "composer wheel leaves transcript following", h.m.State().ScrollOffset, 0)
}

func TestChatMouseWheelOwnsComposerAtExactWrapBoundary(t *testing.T) {
	h := mouseHarness(t)
	// At 80 columns, the prompt leaves 74 cells. The editor needs a cursor
	// row after six exactly full rows even though plain word wrapping does not.
	draft := "START" + strings.Repeat("x", 436) + "END"
	h.input(tea.PasteMsg{Content: draft})
	before := strings.Join(growthComposerRows(t, h), "\n")
	if strings.Contains(before, "START") {
		t.Fatal("fixture must overflow the composer's six visible rows")
	}
	h.input(tea.MouseWheelMsg{X: 10, Y: mouseComposerY(h), Button: tea.MouseWheelUp})
	chatEqual(t, "exact-wrap overflow owns wheel", h.m.State().ScrollOffset, 0)
	v3Contains(t, strings.Join(growthComposerRows(t, h), "\n"), "START")
	h.input(tea.MouseWheelMsg{X: 10, Y: mouseComposerY(h), Button: tea.MouseWheelDown})
	chatEqual(t, "exact-wrap overflow still owns wheel from top", h.m.State().ScrollOffset, 0)
	if strings.Contains(strings.Join(growthComposerRows(t, h), "\n"), "START") {
		t.Fatal("wheel down did not return to the bottom of the composer")
	}
	chatEqual(t, "boundary scroll preserves draft", h.m.State().Draft, draft)
}

func mouseComposerY(h *chatHarness) int {
	h.t.Helper()
	rows := strings.Split(ansi.Strip(h.m.View().Content), "\n")
	for i, row := range rows {
		if strings.HasPrefix(row, "╰") {
			return i - 1
		}
	}
	return len(rows) - 2
}
