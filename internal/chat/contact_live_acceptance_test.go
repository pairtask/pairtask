package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestContactLiveAcceptanceNonDialingContactUpdatesOnLinkEvents(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	h := contactChatHarness(t, contactChatRow(key, "Home Mac", true))
	h.input(chatKey(tea.KeyTab, 0))
	req := h.take(protocol.TypeOperatorContact)
	page := contactMessagesPage(key)
	page.ContactPage.Sync.Dials = false
	page.ContactPage.Sync.Live = false
	h.reply(req, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, h.m.State().Hint, "leaves when they next sync")
	for i, live := range []bool{true, false} {
		h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
			Kind: protocol.EventContactMessagesChanged, ContactKey: key, Watermark: protocol.Watermark{Epoch: "epoch", Revision: uint64(i + 2)},
		}}
		req = h.take(protocol.TypeOperatorContact)
		chatEqual(t, "link event refresh is read-only", req.ContactOp, protocol.ContactOpList)
		page = contactMessagesPage(key)
		page.ContactPage.Sync.Live = live
		h.reply(req, page)
		h.await(func() bool { return h.m.State().InFlight == 0 })
		view := ansi.Strip(h.m.View().Content)
		if live {
			v3Contains(t, h.m.State().Hint, "sent at once")
			v3Contains(t, view, "connected: messages leave at once, both ways")
		} else {
			v3Contains(t, h.m.State().Hint, "leaves when they next sync")
			if strings.Contains(view, "connected: messages leave at once") {
				t.Fatal("closed link still shown as connected")
			}
		}
	}
}
