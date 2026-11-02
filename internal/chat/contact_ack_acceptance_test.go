package chat_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func ackContactRow(cursor int64, state string) protocol.ContactMessageView {
	return protocol.ContactMessageView{ID: fmt.Sprint("in-", cursor), Cursor: cursor, Direction: "in", State: "read", AckState: state, Text: fmt.Sprint("MESSAGE ", cursor), CreatedAt: time.Now()}
}

func ackContactHarness(t *testing.T, available bool, summary protocol.ContactAckSummary, rows ...protocol.ContactMessageView) (*chatHarness, string) {
	t.Helper()
	key := "sha256:" + strings.Repeat("a", 64)
	contact := contactChatRow(key, "remote", true)
	if available {
		contact.Acks = &summary
	}
	h := ackContactsHarness(t, available, contact)
	h.input(chatKey(tea.KeyTab, 0))
	load := h.take(protocol.TypeOperatorContact)
	page := contactMessagesPage(key, rows...)
	if available {
		page.ContactPage.Acks = &summary
	}
	h.reply(load, page)
	return h, key
}

func ackContactsHarness(t *testing.T, available bool, contacts ...protocol.PairingContactView) *chatHarness {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 120, Height: 30})
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	pairing := pairingChatRequest(h, "list")
	h.reply(pairing, protocol.Message{Type: protocol.TypeOperatorPairingResult, PairingResult: &protocol.PairingSnapshot{Contacts: contacts, ContactsTotal: len(contacts), RedeemAvailable: true, AcksAvailable: available}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h
}

type contactAckReply struct {
	count   int
	summary protocol.ContactAckSummary
	rows    []protocol.ContactMessageView
}

func ackContactReply(h *chatHarness, req protocol.Message, result contactAckReply) {
	h.t.Helper()
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorContactResult, ContactKey: req.ContactKey, ContactAcked: &result.count, ContactAcks: &result.summary})
	if len(result.rows) > 0 {
		refresh := h.take(protocol.TypeOperatorContact)
		chatEqual(h.t, "recheck reads authoritative ACK states", refresh.ContactOp, protocol.ContactOpList)
		chatEqual(h.t, "recheck explicitly targets selected older rows", refresh.ContactBefore > 0, true)
		page := contactMessagesPage(req.ContactKey, result.rows...)
		page.ContactPage.Acks = &result.summary
		h.reply(refresh, page)
	}
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func TestContactAckChatAcceptanceCursorSelectsPrivateInboundID(t *testing.T) {
	h, key := ackContactHarness(t, true, protocol.ContactAckSummary{Unacked: 2, AckUpto: 91}, ackContactRow(91, "needs_ack"), ackContactRow(37, "needs_ack"))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Type(h, "kept draft")
	v2Command(h, "/ack #37")
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "private contact operation", req.ContactOp, protocol.ContactOpAck)
	chatEqual(t, "canonical contact", req.ContactKey, key)
	chatEqual(t, "one ID", len(req.ContactAck), 1)
	chatEqual(t, "cursor resolves to ID", req.ContactAck[0], "in-37")
	chatEqual(t, "not a bulk bound", req.ContactAckUpto, int64(0))
	chatEqual(t, "not a review approval", req.ContactApprove == nil, true)
	ackContactReply(h, req, contactAckReply{count: 1, summary: protocol.ContactAckSummary{Unacked: 1, AckUpto: 91}, rows: []protocol.ContactMessageView{ackContactRow(37, "acked")}})
	chatEqual(t, "draft retained", h.m.State().Draft, "kept draft")
	v3Contains(t, ansi.Strip(h.m.View().Content), "1 need ack")
}

func TestContactAckChatAcceptanceRenderFocusAndLegacyReadNeverHumanAck(t *testing.T) {
	row := ackContactRow(13, "needs_ack")
	row.State = "received"
	h, _ := ackContactHarness(t, true, protocol.ContactAckSummary{Unacked: 1, AckUpto: 13}, row)
	read := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "display preserves transport read", read.ContactOp, protocol.ContactOpRead)
	contactReadReply(h, read)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	for i := 0; i < 5; i++ {
		h.m.View()
	}
	h.input(chatKey(tea.KeyTab, tea.ModShift))
	h.input(chatKey(tea.KeyTab, 0))
	chatEqual(t, "render and focus send no human ACK", h.m.State().InFlight, 0)
	v3Contains(t, ansi.Strip(h.m.View().Content), "1 need ack")
}

func TestContactAckChatAcceptanceUnsupportedBrokerExplainsWithoutSending(t *testing.T) {
	h, _ := ackContactHarness(t, false, protocol.ContactAckSummary{}, ackContactRow(13, ""))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/ack all")
	chatEqual(t, "old broker gets no ACK request", h.m.State().InFlight, 0)
	v3Contains(t, ansi.Strip(h.m.View().Content), "this broker can't track acknowledgments in ⇄ yet")
}

func TestContactAckChatAcceptancePickerExcludesBeforeExpiredAndAcknowledged(t *testing.T) {
	h, _ := ackContactHarness(t, true, protocol.ContactAckSummary{Unacked: 1, AckUpto: 9, AckExpired: 1}, ackContactRow(9, "needs_ack"), ackContactRow(8, "acked"), ackContactRow(7, "expired"), ackContactRow(6, "before"))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/ack")
	p := h.m.State().Picker
	if p == nil {
		t.Fatal("contact ACK picker missing")
	}
	for _, item := range p.Items {
		if item != "all" && item != "#9" {
			t.Fatalf("ineligible contact picker choice %q", item)
		}
	}
	if len(p.Items) != 2 {
		t.Fatalf("picker choices %v, want all and #9", p.Items)
	}
	h.input(chatKey(tea.KeyEscape, 0))
	chatEqual(t, "picker alone acknowledges nothing", h.m.State().InFlight, 0)
	v3Contains(t, ansi.Strip(h.m.View().Content), "1 expired")
}

func TestContactAckChatAcceptanceNarrowFramesKeepHonestMarks(t *testing.T) {
	h, _ := ackContactHarness(t, true, protocol.ContactAckSummary{Unacked: 1, AckUpto: 900, AckExpired: 1}, ackContactRow(900, "needs_ack"), ackContactRow(899, "acked"), ackContactRow(898, "expired"), ackContactRow(897, "before"))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	wide := ansi.Strip(h.m.View().Content)
	for _, want := range []string{"#900 ○ needs ack", "#899 ● acknowledged", "#898 expired unacknowledged"} {
		v3Contains(t, wide, want)
	}
	if strings.Contains(wide, "#897 ●") || strings.Contains(wide, "#897 ○") {
		t.Fatal("before-tracking row falsely marked")
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 45, Height: 20}, {Width: 80, Height: 24}} {
		h.input(size)
		v2FrameFits(t, h, size.Width, size.Height)
	}
}
