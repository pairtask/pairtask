package chat_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
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

func contactAckRefresh(h *chatHarness, key string, page protocol.Message) {
	h.t.Helper()
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventContactMessagesChanged, ContactKey: key, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	refresh := h.take(protocol.TypeOperatorContact)
	chatEqual(h.t, "event refreshes contact", refresh.ContactOp, protocol.ContactOpList)
	h.reply(refresh, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func TestContactAckChatRecoveryAcceptanceDelayedPageCannotRegressTerminalMark(t *testing.T) {
	for _, tc := range []struct{ state, mark string }{{"acked", "● acknowledged"}, {"expired", "expired unacknowledged"}} {
		t.Run(tc.state, func(t *testing.T) {
			row := ackContactRow(37, tc.state)
			stamp := time.Now()
			if tc.state == "acked" {
				row.AckedAt = &stamp
			}
			h, key := ackContactHarness(t, true, protocol.ContactAckSummary{}, row)
			h.await(func() bool { return h.m.State().InFlight == 0 })
			stale := contactMessagesPage(key, ackContactRow(37, "needs_ack"))
			stale.ContactPage.Acks = &protocol.ContactAckSummary{Unacked: 1, AckUpto: 37}
			contactAckRefresh(h, key, stale)
			v3Contains(t, ansi.Strip(h.m.View().Content), "#37 "+tc.mark)
			v2Command(h, "/ack #37")
			chatEqual(t, "terminal row cannot be acknowledged again", h.m.State().InFlight, 0)
		})
	}
}

func TestContactAckChatRecoveryAcceptanceOlderLoadedAckUpdatesMarkOutsideRefresh(t *testing.T) {
	h, key := ackContactHarness(t, true, protocol.ContactAckSummary{Unacked: 2, AckUpto: 91}, ackContactRow(91, "needs_ack"), ackContactRow(37, "needs_ack"))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/ack #37")
	req := h.take(protocol.TypeOperatorContact)
	ackContactReply(h, req, contactAckReply{count: 1, summary: protocol.ContactAckSummary{Unacked: 1, AckUpto: 91}, rows: []protocol.ContactMessageView{ackContactRow(37, "acked")}})
	newest := contactMessagesPage(key, ackContactRow(91, "needs_ack"))
	newest.ContactPage.More = true
	newest.ContactPage.Acks = &protocol.ContactAckSummary{Unacked: 1, AckUpto: 91}
	contactAckRefresh(h, key, newest)
	v3Contains(t, ansi.Strip(h.m.View().Content), "#37 ● acknowledged")
	v2Command(h, "/ack")
	ackPickerItems(t, h, "all", "#91")
}

func contactAckExpiryRace(h *chatHarness, command string) protocol.Message {
	h.t.Helper()
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, command)
	return h.take(protocol.TypeOperatorContact)
}

func contactAckNotInvented(t *testing.T, h *chatHarness) {
	t.Helper()
	if strings.Contains(ansi.Strip(h.m.View().Content), "#37 ● acknowledged") {
		t.Fatal("ACK result falsely acknowledged a message that expired before commit")
	}
}

func TestContactAckChatRecoveryAcceptanceZeroAckNeverFabricatesAcknowledgment(t *testing.T) {
	h, _ := ackContactHarness(t, true, protocol.ContactAckSummary{Unacked: 2, AckUpto: 91}, ackContactRow(91, "needs_ack"), ackContactRow(37, "needs_ack"))
	req := contactAckExpiryRace(h, "/ack #37")
	contactAckZeroReply(h, req, protocol.ContactAckSummary{Unacked: 1, AckUpto: 91, AckExpired: 1}, ackContactRow(37, "expired"))
	contactAckNotInvented(t, h)
	v3Contains(t, ansi.Strip(h.m.View().Content), "#37 expired unacknowledged")
}

func contactAckZeroReply(h *chatHarness, req protocol.Message, summary protocol.ContactAckSummary, row protocol.ContactMessageView) {
	h.t.Helper()
	zero := 0
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorContactResult, ContactKey: req.ContactKey, ContactAcked: &zero, ContactAcks: &summary})
	refresh := h.take(protocol.TypeOperatorContact)
	chatEqual(h.t, "zero result still reads authoritative state", refresh.ContactOp, protocol.ContactOpList)
	chatEqual(h.t, "zero result rechecks older selected row", refresh.ContactBefore, int64(38))
	contactAckNotInvented(h.t, h)
	page := contactMessagesPage(req.ContactKey, row)
	page.ContactPage.Acks = &summary
	h.reply(refresh, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func TestContactAckChatRecoveryAcceptanceZeroReplayRefreshesOlderAcknowledgedRow(t *testing.T) {
	h, _ := ackContactHarness(t, true, protocol.ContactAckSummary{Unacked: 2, AckUpto: 91}, ackContactRow(91, "needs_ack"), ackContactRow(37, "needs_ack"))
	req := contactAckExpiryRace(h, "/ack #37")
	contactAckZeroReply(h, req, protocol.ContactAckSummary{Unacked: 1, AckUpto: 91}, ackContactRow(37, "acked"))
	v3Contains(t, ansi.Strip(h.m.View().Content), "#37 ● acknowledged")
	v2Command(h, "/ack")
	ackPickerItems(t, h, "all", "#91")
}

func TestContactAckChatRecoveryAcceptancePartialBulkUsesAuthoritativeStates(t *testing.T) {
	h, _ := ackContactHarness(t, true, protocol.ContactAckSummary{Unacked: 2, AckUpto: 91}, ackContactRow(91, "needs_ack"), ackContactRow(37, "needs_ack"))
	req := contactAckExpiryRace(h, "/ack all")
	acked := 1
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorContactResult, ContactKey: req.ContactKey, ContactAcked: &acked, ContactAcks: &protocol.ContactAckSummary{AckExpired: 1}})
	refresh := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "partial success refreshes rather than guessing", refresh.ContactOp, protocol.ContactOpList)
	contactAckNotInvented(t, h)
	page := contactMessagesPage(req.ContactKey, ackContactRow(91, "acked"), ackContactRow(37, "expired"))
	page.ContactPage.Acks = &protocol.ContactAckSummary{AckExpired: 1}
	h.reply(refresh, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	contactAckNotInvented(t, h)
	v3Contains(t, ansi.Strip(h.m.View().Content), "#37 expired unacknowledged")
}

func TestContactAckChatRecoveryAcceptanceRecheckPagesThroughOlderSelection(t *testing.T) {
	h, key := ackContactHarness(t, true, protocol.ContactAckSummary{Unacked: 2, AckUpto: 91}, ackContactRow(91, "needs_ack"), ackContactRow(37, "needs_ack"))
	req := contactAckExpiryRace(h, "/ack all")
	acked := 2
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorContactResult, ContactKey: key, ContactAcked: &acked, ContactAcks: &protocol.ContactAckSummary{}})
	first := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "recheck starts just above newest selected cursor", first.ContactBefore, int64(92))
	page := contactMessagesPage(key, ackContactRow(91, "acked"))
	page.ContactPage.More = true
	h.reply(first, page)
	second := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "recheck continues below page boundary", second.ContactBefore, int64(91))
	h.reply(second, contactMessagesPage(key, ackContactRow(37, "acked")))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "#37 ● acknowledged")
	v3Contains(t, view, "#91 ● acknowledged")
}
