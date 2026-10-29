package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
	"strings"
	"testing"
)

func TestContactMessagesErrorsAcceptanceListRefusalDoesNotSpin(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	h := contactChatHarness(t, contactChatRow(key, "remote", true))
	h.input(chatKey(tea.KeyTab, 0))
	req := h.take(protocol.TypeOperatorContact)
	h.reply(req, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "temporarily unavailable"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(tea.WindowSizeMsg{Width: 100, Height: 25})
	chatEqual(t, "no automatic retry loop", h.m.State().InFlight, 0)
}
func TestContactMessagesErrorsAcceptanceReadRefusalRetriesAfterReload(t *testing.T) {
	h, key := contactMessagesHarness(t, contactUnreadRows(1)...)
	read := h.take(protocol.TypeOperatorContact)
	h.reply(read, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "temporarily unavailable"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventContactMessagesChanged, ContactKey: key, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	reload := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "event reload", reload.ContactOp, protocol.ContactOpList)
	h.reply(reload, contactMessagesPage(key, contactUnreadRows(1)...))
	retry := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "read retry after refreshed state", retry.ContactOp, protocol.ContactOpRead)
	chatEqual(t, "one retry ID", len(retry.ContactRead), 1)
	chatEqual(t, "same retry ID", retry.ContactRead[0], "incoming-1")
}
