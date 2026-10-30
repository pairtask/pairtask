package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestContactDestinationAcceptanceNamedTabShowsDialAddress(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	c := contactChatRow(key, "My host", true)
	c.Destination = "192.168.1.32:7445"
	h := contactChatHarness(t, c)
	h.input(chatKey(tea.KeyTab, 0))
	req := h.take(protocol.TypeOperatorContact)
	page := contactMessagesPage(key)
	page.ContactPage.Sync.Dials = true
	h.reply(req, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "My host")
	v3Contains(t, view, "192.168.1.32:7445")
	if strings.Contains(view, "connected: messages leave at once") {
		t.Fatal("a dialable address alone was shown as a live connection")
	}
}
