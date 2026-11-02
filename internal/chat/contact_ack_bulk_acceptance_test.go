package chat_test

import (
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestContactAckChatBulkAcceptanceUsesSummaryBoundNotLoadedRows(t *testing.T) {
	h, key := ackContactHarness(t, true, protocol.ContactAckSummary{Unacked: 80, AckUpto: 900}, ackContactRow(900, "needs_ack"), ackContactRow(899, "needs_ack"))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/ack all")
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "private ACK", req.ContactOp, protocol.ContactOpAck)
	chatEqual(t, "contact", req.ContactKey, key)
	chatEqual(t, "frozen summary upper bound", req.ContactAckUpto, int64(900))
	chatEqual(t, "bulk is not just loaded IDs", len(req.ContactAck), 0)
	ackContactReply(h, req, 80, protocol.ContactAckSummary{}, ackContactRow(900, "acked"), ackContactRow(899, "acked"))
}

func TestContactAckChatBulkAcceptanceContactsUsesOnlyFrozenNonemptyContacts(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	other := "sha256:" + strings.Repeat("b", 64)
	empty := "sha256:" + strings.Repeat("c", 64)
	first := contactChatRow(key, "first", true)
	first.Acks = &protocol.ContactAckSummary{Unacked: 2, AckUpto: 70}
	second := contactChatRow(other, "second", true)
	second.Acks = &protocol.ContactAckSummary{Unacked: 5, AckUpto: 150}
	third := contactChatRow(empty, "empty", true)
	third.Acks = &protocol.ContactAckSummary{}
	h := ackContactsHarness(t, true, first, second, third)
	v2Command(h, "/ack contacts")
	seen := map[string]int64{}
	for i := 0; i < 2; i++ {
		req := h.take(protocol.TypeOperatorContact)
		chatEqual(t, "bulk contact ACK", req.ContactOp, protocol.ContactOpAck)
		if _, duplicate := seen[req.ContactKey]; duplicate {
			t.Fatalf("duplicate contact ACK %s", req.ContactKey)
		}
		seen[req.ContactKey] = req.ContactAckUpto
		h.reply(req, protocol.Message{Type: protocol.TypeOperatorContactResult, ContactKey: req.ContactKey, ContactAcked: new(int), ContactAcks: &protocol.ContactAckSummary{}})
	}
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "first frozen bound", seen[key], int64(70))
	chatEqual(t, "second frozen bound", seen[other], int64(150))
	chatEqual(t, "empty contact skipped", len(seen), 2)
}

func TestContactAckChatBulkAcceptanceLocalAllKeepsLocalScope(t *testing.T) {
	contact := contactChatRow("sha256:"+strings.Repeat("a", 64), "remote", true)
	contact.Acks = &protocol.ContactAckSummary{Unacked: 3, AckUpto: 99}
	h := ackContactsHarness(t, true, contact)
	v2Command(h, "/ack all")
	req := h.take(protocol.TypeOperatorInboxStatus)
	chatEqual(t, "local all uses local inbox status", req.Type, protocol.TypeOperatorInboxStatus)
	chatEqual(t, "local all has no contact", req.ContactKey, "")
	chatEqual(t, "local all has no private contact selector", req.ContactAckUpto, int64(0))
}
