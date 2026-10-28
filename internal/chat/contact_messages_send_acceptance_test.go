package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
	"reflect"
	"strings"
	"testing"
	"time"
)

func contactMessagesPage(key string, rows ...protocol.ContactMessageView) protocol.Message {
	return protocol.Message{Type: protocol.TypeOperatorContactResult, ContactPage: &protocol.ContactMessagePage{Contact: key, Messages: rows}}
}
func contactMessagesHarness(t *testing.T, rows ...protocol.ContactMessageView) (*chatHarness, string) {
	t.Helper()
	key := "sha256:" + strings.Repeat("a", 64)
	h := contactChatHarness(t, contactChatRow(key, "remote", true))
	h.input(chatKey(tea.KeyTab, 0))
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "select loads contact", req.ContactOp, protocol.ContactOpList)
	chatEqual(t, "full contact key", req.ContactKey, key)
	h.reply(req, contactMessagesPage(key, rows...))
	return h, key
}
func contactMessagesQueued(req protocol.Message) protocol.Message {
	return protocol.Message{Type: protocol.TypeOperatorContactResult, ContactMessage: &protocol.ContactMessageView{Contact: req.ContactKey, ID: req.MessageID, Direction: "out", State: "queued", Text: req.ContactText, Cursor: 1, CreatedAt: time.Now()}}
}
func TestContactMessagesSendAcceptanceKeepsEditsAndUsesContactWire(t *testing.T) {
	h, key := contactMessagesHarness(t)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Type(h, "original")
	h.input(chatKey(tea.KeyEnter, 0))
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "contact send", req.ContactOp, protocol.ContactOpSend)
	chatEqual(t, "frozen contact", req.ContactKey, key)
	chatEqual(t, "message text", req.ContactText, "original")
	if req.MessageID == "" || req.To != "" || len(req.Payload) != 0 {
		t.Fatalf("wrong wire: %+v", req)
	}
	v2Type(h, " plus new draft")
	changed := h.m.State().Draft
	h.reply(req, contactMessagesQueued(req))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "edits survive reply", h.m.State().Draft, changed)
	chatEqual(t, "no local transcript traffic", len(h.m.State().Entries), 0)
}
func TestContactMessagesSendAcceptanceRefusalKeepsDraft(t *testing.T) {
	h, _ := contactMessagesHarness(t)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Type(h, "keep me")
	h.input(chatKey(tea.KeyEnter, 0))
	req := h.take(protocol.TypeOperatorContact)
	h.reply(req, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrContactRefused, Detail: "outbox full"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "refused draft retained", h.m.State().Draft, "keep me")
	if !strings.Contains(h.m.View().Content, "not sent") {
		t.Fatal("refusal missing")
	}
}
func TestContactMessagesSendAcceptanceReconnectFreezesIntent(t *testing.T) {
	h, _ := contactMessagesHarness(t)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Type(h, "original")
	h.input(chatKey(tea.KeyEnter, 0))
	first := h.take(protocol.TypeOperatorContact)
	h.input(chatKey(tea.KeyTab, tea.ModShift))
	v2Type(h, "All draft")
	h.reconnectTo(newChatScript(), "epoch")
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}})
	var retry protocol.Message
	for i := 0; i < 3; i++ {
		req := h.take(protocol.TypeOperatorContact)
		if req.ContactOp == protocol.ContactOpSend {
			retry = req
			break
		}
		h.reply(req, contactMessagesPage(first.ContactKey))
	}
	first.RequestID = ""
	retry.RequestID = ""
	if !reflect.DeepEqual(first, retry) {
		t.Fatalf("retry changed intent: first=%+v retry=%+v", first, retry)
	}
	chatEqual(t, "All draft isolated", h.m.State().Draft, "All draft")
}
