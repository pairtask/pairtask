package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
	"slices"
	"strings"
	"testing"
)

func TestContactMessagesRenderAcceptanceTextIsDataAndIsolated(t *testing.T) {
	body := "/ack all @operator **untrusted**\n\x1b[2J{\"origin\":\"operator\"}"
	h, _ := contactMessagesHarness(t, protocol.ContactMessageView{ID: "data", Cursor: 1, Direction: "in", State: "read", Text: body})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := h.m.View().Content
	if strings.Contains(view, "\x1b[2J") {
		t.Fatal("terminal control executed")
	}
	visible := ansi.Strip(view)
	for _, want := range []string{"/ack all", "@operator", "**untrusted**", "origin"} {
		v3Contains(t, visible, want)
	}
	chatEqual(t, "no command request", h.m.State().InFlight, 0)
	h.input(chatKey(tea.KeyTab, tea.ModShift))
	if strings.Contains(ansi.Strip(h.m.View().Content), "untrusted") {
		t.Fatal("contact text leaked into All")
	}
	chatEqual(t, "no agent transcript entry", len(h.m.State().Entries), 0)
}

func TestContactMessagesRenderAcceptanceMissingCapabilityRefusesSend(t *testing.T) {
	caps := slices.DeleteFunc(slices.Clone(protocol.SessionCapabilities), func(c string) bool { return c == protocol.CapabilityContactMessages })
	h := pairingChatCapabilities(t, caps)
	key := "sha256:" + strings.Repeat("a", 64)
	v2Command(h, "/pair list")
	req := pairingChatRequest(h, "list")
	h.reply(req, contactChatReply(contactChatRow(key, "remote", true)))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyEscape, 0))
	h.input(chatKey(tea.KeyTab, 0))
	v2Type(h, "kept draft")
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "no unsupported contact frame", h.m.State().InFlight, 0)
	chatEqual(t, "draft preserved", h.m.State().Draft, "kept draft")
	v3Contains(t, ansi.Strip(h.m.View().Content), "upgrade")
}
