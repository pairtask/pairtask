package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"fmt"
	"github.com/pairtask/pairtask/internal/protocol"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func contactUnreadRows(n int) []protocol.ContactMessageView {
	rows := make([]protocol.ContactMessageView, 0, n)
	for i := n; i > 0; i-- {
		rows = append(rows, protocol.ContactMessageView{ID: fmt.Sprint("incoming-", i), Cursor: int64(i), Direction: "in", State: "received", Text: fmt.Sprint("VISIBLE MESSAGE ", i), CreatedAt: time.Now()})
	}
	return rows
}
func contactReadReply(h *chatHarness, req protocol.Message) {
	h.t.Helper()
	marked := len(req.ContactRead)
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorContactResult, ContactMarked: &marked})
}
func TestContactMessagesReadAcceptanceOnlyVisibleRows(t *testing.T) {
	h, _ := contactMessagesHarness(t, contactUnreadRows(40)...)
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "visible read", req.ContactOp, protocol.ContactOpRead)
	if len(req.ContactRead) == 0 || len(req.ContactRead) >= 40 || !slices.Contains(req.ContactRead, "incoming-40") || slices.Contains(req.ContactRead, "incoming-1") {
		t.Fatalf("offscreen read set: %v", req.ContactRead)
	}
	contactReadReply(h, req)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	for i := 0; i < 10; i++ {
		h.m.View()
	}
	chatEqual(t, "render has no side effects", h.m.State().InFlight, 0)
	h.input(chatKey(tea.KeyPgUp, 0))
	older := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "scroll reads", older.ContactOp, protocol.ContactOpRead)
	for _, id := range older.ContactRead {
		if slices.Contains(req.ContactRead, id) {
			t.Fatalf("re-read confirmed %s", id)
		}
	}
}
func TestContactMessagesReadAcceptanceHiddenTabDoesNotReadRefresh(t *testing.T) {
	h, key := contactMessagesHarness(t)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyTab, tea.ModShift))
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventContactMessagesChanged, ContactKey: key, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "refresh only", req.ContactOp, protocol.ContactOpList)
	h.reply(req, contactMessagesPage(key, contactUnreadRows(1)...))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "hidden contact stays hidden", h.m.State().SelectedAll, true)
	chatEqual(t, "no leaked local messages", len(h.m.State().Entries), 0)
	h.input(chatKey(tea.KeyTab, 0))
	read := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "read on display", read.ContactOp, protocol.ContactOpRead)
	if !reflect.DeepEqual(read.ContactRead, []string{"incoming-1"}) {
		t.Fatalf("read set: %v", read.ContactRead)
	}
}
func TestContactMessagesReadAcceptanceReconnectRetriesExactSet(t *testing.T) {
	h, _ := contactMessagesHarness(t, contactUnreadRows(3)...)
	first := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "read", first.ContactOp, protocol.ContactOpRead)
	h.input(chatKey(tea.KeyTab, tea.ModShift))
	h.reconnectTo(newChatScript(), "epoch")
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}})
	var retry protocol.Message
	for i := 0; i < 3; i++ {
		req := h.take(protocol.TypeOperatorContact)
		if req.ContactOp == protocol.ContactOpRead {
			retry = req
			break
		}
		h.reply(req, contactMessagesPage(first.ContactKey))
	}
	first.RequestID = ""
	retry.RequestID = ""
	if !reflect.DeepEqual(first, retry) {
		t.Fatalf("changed retry: %+v vs %+v", first, retry)
	}
}

func TestContactMessagesReadAcceptanceCardOnlyMarksNothing(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	h := contactChatHarness(t, contactChatRow(key, "remote", true))
	h.input(tea.WindowSizeMsg{Width: 120, Height: 6})
	h.input(chatKey(tea.KeyTab, 0))
	req := h.take(protocol.TypeOperatorContact)
	h.reply(req, contactMessagesPage(key, contactUnreadRows(1)...))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "card-only view has no read request", h.m.State().InFlight, 0)
	h.input(tea.WindowSizeMsg{Width: 120, Height: 30})
	read := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "read after message becomes visible", read.ContactOp, protocol.ContactOpRead)
}
