package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
	"strings"
	"testing"
)

func TestContactMessagesPagingAcceptanceRefreshFromEmptyFindsOlderRows(t *testing.T) {
	h, key := contactMessagesHarness(t)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventContactMessagesChanged, ContactKey: key, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	refresh := h.take(protocol.TypeOperatorContact)
	rows := contactUnreadRows(40)
	for i := range rows {
		rows[i].State = "read"
		rows[i].Cursor += 100
	}
	page := contactMessagesPage(key, rows...)
	page.ContactPage.More = true
	h.reply(refresh, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	for i := 0; i < 10 && h.m.State().InFlight == 0; i++ {
		h.input(chatKey(tea.KeyPgUp, 0))
	}
	older := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "older operation", older.ContactOp, protocol.ContactOpList)
	chatEqual(t, "last displayed cursor", older.ContactBefore, int64(101))
}

func TestContactMessagesPagingAcceptanceDisconnectedPageRestartsBoundary(t *testing.T) {
	old := protocol.ContactMessageView{ID: "old", Cursor: 1, Direction: "in", State: "read", Text: "OLD UNIQUE TEXT"}
	h, key := contactMessagesHarness(t, old)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventContactMessagesChanged, ContactKey: key, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	refresh := h.take(protocol.TypeOperatorContact)
	newest := protocol.ContactMessageView{ID: "new", Cursor: 100, Direction: "in", State: "read", Text: "NEW UNIQUE TEXT"}
	page := contactMessagesPage(key, newest)
	page.ContactPage.More = true
	h.reply(refresh, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	if strings.Contains(h.m.View().Content, "OLD UNIQUE TEXT") {
		t.Fatal("disconnected old page shown as continuous")
	}
	h.input(chatKey(tea.KeyPgUp, 0))
	older := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "fetch gap from new boundary", older.ContactBefore, int64(100))
	middle := protocol.ContactMessageView{ID: "middle", Cursor: 50, Direction: "in", State: "read", Text: "MIDDLE UNIQUE TEXT"}
	h.reply(older, contactMessagesPage(key, middle, old))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := h.m.View().Content
	for _, text := range []string{"OLD UNIQUE TEXT", "MIDDLE UNIQUE TEXT", "NEW UNIQUE TEXT"} {
		if strings.Count(view, text) != 1 {
			t.Fatalf("missing/duplicate %s: %s", text, view)
		}
	}
}
