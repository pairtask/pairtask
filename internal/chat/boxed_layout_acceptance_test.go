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

func footerRow(h *chatHarness) string {
	h.t.Helper()
	rows := strings.Split(h.m.View().Content, "\n")
	return rows[len(rows)-1]
}

func TestChatBoxFooterFramesDraftAtNormalSizes(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {40, 12}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			h := v3DisplayHarness(t, false)
			h.input(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			v2Type(h, "kept draft")
			rows := strings.Split(ansi.Strip(h.m.View().Content), "\n")
			chatEqual(t, "full frame height", len(rows), size[1])
			v3Contains(t, rows[len(rows)-1], "connected")
			if !strings.HasPrefix(rows[len(rows)-2], "╰") {
				t.Fatalf("box bottom is not directly above footer: %q", rows)
			}
			v3Contains(t, rows[len(rows)-3], "│ ")
			v3Contains(t, rows[len(rows)-3], "kept draft")
			v3Contains(t, rows[len(rows)-4], "to everyone")
			v3Contains(t, rows[len(rows)-4], "╭")
			if strings.Contains(rows[0], "connected") {
				t.Fatal("connection facts remained in title instead of footer")
			}
			v2FrameFits(t, h, size[0], size[1])
		})
	}
}

func TestChatBoxFooterShowsRefusalInsteadOfKeyHints(t *testing.T) {
	h := v3DisplayHarness(t, false)
	h.input(tea.WindowSizeMsg{Width: 80, Height: 24})
	v2Command(h, "/ack #999")
	h.input(chatKey(tea.KeyEscape, 0))
	footer := ansi.Strip(footerRow(h))
	v3Contains(t, footer, h.m.State().Status)
	if strings.Contains(footer, "Enter send") {
		t.Fatal("key hints displaced the refusal")
	}
	v3Contains(t, ansi.Strip(h.m.View().Content), "to everyone")
	v2FrameFits(t, h, 80, 24)

	h.input(tea.WindowSizeMsg{Width: 160, Height: 24})
	wideFooter := ansi.Strip(footerRow(h))
	v3Contains(t, wideFooter, h.m.State().Status)
	if strings.Contains(wideFooter, "Enter send") {
		t.Fatal("active status must replace hints even when the footer has room")
	}
}

func TestChatBoxFooterKeepsAckWarningAndShortStatusVisible(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 80, Height: 24})
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{
		Watermark: protocol.Watermark{Epoch: "epoch", Revision: 1}, InboxPending: 12, InboxHeadSeq: 12,
	}})
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	status := h.take(protocol.TypeOperatorInboxStatus)
	usage := bulkUsage(12)
	usage.HeadSeq = 12
	h.reply(status, protocol.Message{Type: protocol.TypeOperatorInboxStatus, Inbox: &usage})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/reply cancel")
	footer := footerRow(h)
	for _, text := range []string{"12 need ack", "/ack all", "12/16", "reply cancelled"} {
		v3Contains(t, ansi.Strip(footer), text)
	}
	formattingWarningStyle(t, v3StyleAt(t, footer, "12/16"))
	chatEqual(t, "rendering cannot acknowledge", len(h.m.State().QueuedAcks), 0)
	v2FrameFits(t, h, 80, 24)
}

func TestChatBoxTinyFramesKeepTranscriptAudienceDraftAndFooter(t *testing.T) {
	h := v3DisplayHarness(t, false)
	v2Type(h, "DRAFT")
	for _, size := range [][2]int{{80, 8}, {80, 6}, {80, 4}, {20, 4}} {
		h.input(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		frame := ansi.Strip(h.m.View().Content)
		for _, text := range []string{"needs ack", "to everyone", "DRAFT"} {
			v3Contains(t, frame, text)
		}
		v3Contains(t, ansi.Strip(footerRow(h)), "connected")
		v2FrameFits(t, h, size[0], size[1])
	}
}

func TestChatBoxPickerIsBelowInputAndAboveFooter(t *testing.T) {
	h := v3DisplayHarness(t, false)
	h.input(chatKey('k', tea.ModCtrl))
	rows := strings.Split(ansi.Strip(h.m.View().Content), "\n")
	bottom, picker := -1, -1
	for i, row := range rows {
		if strings.HasPrefix(row, "╰") {
			bottom = i
		}
		if strings.Contains(row, "/agents") && strings.Contains(row, "list agents") {
			picker = i
		}
	}
	if bottom < 0 {
		t.Fatal("input box bottom is missing")
	}
	if picker <= bottom || picker >= len(rows)-1 {
		t.Fatalf("input, picker and footer order is wrong: %q", rows)
	}
	v3Contains(t, rows[len(rows)-1], "connected")
	v2FrameFits(t, h, 120, 24)
}

func TestChatBoxFooterWheelDoesNotMoveDraftCursor(t *testing.T) {
	h := mouseHarness(t)
	draft := "row01\nrow02\nrow03\nrow04\nrow05\nrow06\nrow07\nrow08\nrow09"
	h.input(tea.PasteMsg{Content: draft})
	before := strings.Join(growthComposerRows(t, h), "\n")
	h.input(tea.MouseWheelMsg{X: 10, Y: 23, Button: tea.MouseWheelUp})
	chatEqual(t, "footer wheel scrolls transcript", h.m.State().ScrollOffset, 3)
	chatEqual(t, "footer wheel leaves composer viewport", strings.Join(growthComposerRows(t, h), "\n"), before)
	chatEqual(t, "footer wheel leaves draft", h.m.State().Draft, draft)
}
