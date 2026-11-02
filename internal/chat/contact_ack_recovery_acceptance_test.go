package chat_test

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestContactAckChatRecoveryAcceptanceRetriesExactFrozenRequest(t *testing.T) {
	h, _ := ackContactHarness(t, true, protocol.ContactAckSummary{Unacked: 80, AckUpto: 900}, ackContactRow(900, "needs_ack"))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/ack all")
	first := h.take(protocol.TypeOperatorContact)
	h.input(chatKey(tea.KeyTab, tea.ModShift))
	h.reconnectTo(newChatScript(), "epoch")
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}})
	var retry protocol.Message
	for i := 0; i < 3; i++ {
		req := h.take(protocol.TypeOperatorContact)
		if req.ContactOp == protocol.ContactOpAck {
			retry = req
			break
		}
		h.reply(req, contactMessagesPage(first.ContactKey))
	}
	first.RequestID = ""
	retry.RequestID = ""
	if !reflect.DeepEqual(first, retry) {
		t.Fatalf("ACK retry changed selection or durable ID: first=%+v retry=%+v", first, retry)
	}
}

func TestContactAckChatRecoveryAcceptanceRefusalKeepsMessageEligible(t *testing.T) {
	h, _ := ackContactHarness(t, true, protocol.ContactAckSummary{Unacked: 1, AckUpto: 42}, ackContactRow(42, "needs_ack"))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/ack #42")
	first := h.take(protocol.TypeOperatorContact)
	h.reply(first, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "injected failure"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/ack #42")
	second := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "failed ACK remains eligible", second.ContactAck[0], "in-42")
	chatEqual(t, "failure cannot widen bound", second.ContactAckUpto, int64(0))
}
