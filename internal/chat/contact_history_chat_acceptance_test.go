package chat_test

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func historyChatRow(epoch, cursor int64) protocol.ContactMessageView {
	row := contactLayoutRow("same-history-id", cursor)
	row.PairingEpoch = epoch
	return row
}

func historyChatHarness(t *testing.T, rows ...protocol.ContactMessageView) (*chatHarness, string) {
	t.Helper()
	h := pairingChatCapabilities(t, protocol.SessionCapabilitiesFor(25))
	h.input(tea.WindowSizeMsg{Width: 180, Height: 60})
	key := "sha256:" + strings.Repeat("a", 64)
	contact := contactChatRow(key, "Peer", true)
	summary := protocol.ContactAckSummary{Unacked: 1, AckUpto: 2}
	contact.Acks = &summary
	v2Command(h, "/pair status")
	req := pairingChatRequest(h, "status")
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorPairingResult, PairingResult: &protocol.PairingSnapshot{Contacts: []protocol.PairingContactView{contact}, ContactsTotal: 1, AcksAvailable: true}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyEscape, 0))
	h.input(chatKey(tea.KeyTab, 0))
	req = h.take(protocol.TypeOperatorContact)
	chatEqual(t, "select loads contact", req.ContactOp, protocol.ContactOpList)
	page := contactMessagesPage(key, rows...)
	page.ContactPage.Acks = &summary
	h.reply(req, page)
	return h, key
}

func TestContactHistoryChatAcceptanceReusedIDPreservesBothBodies(t *testing.T) {
	old, current := historyChatRow(1, 1), historyChatRow(2, 2)
	old.Text, current.Text = "OLD PAIRING BODY", "NEW PAIRING BODY"
	h, _ := historyChatHarness(t, current, old)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, old.Text)
	v3Contains(t, view, current.Text)
}

func TestContactHistoryChatAcceptanceReplyParentUsesSameEpoch(t *testing.T) {
	old, current := historyChatRow(1, 1), historyChatRow(2, 2)
	old.From, current.From = "old-parent", "new-parent"
	reply := contactLayoutRow("old-reply", 3)
	reply.PairingEpoch, reply.ReplyID, reply.ReplySide = 1, old.ID, protocol.ReplyBySender
	h, _ := historyChatHarness(t, reply, old, current)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "↳ re "+contactLayoutAt.Local().Format("15:04")+" old-parent@Peer")
}

func TestContactHistoryChatAcceptanceReplyNeverBorrowsOtherEpochParent(t *testing.T) {
	parent := historyChatRow(2, 2)
	parent.From = "wrong-epoch-parent"
	reply := contactLayoutRow("old-reply", 3)
	reply.PairingEpoch, reply.ReplyID, reply.ReplySide = 1, parent.ID, protocol.ReplyBySender
	h, _ := historyChatHarness(t, reply, parent)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "↳ re an earlier message")
	if strings.Contains(view, "↳ re "+contactLayoutAt.Local().Format("15:04")) {
		t.Fatal("reply borrowed a parent from another pairing")
	}
}

func TestContactHistoryChatAcceptanceVisibleReadsAreGroupedByEpoch(t *testing.T) {
	old, current := historyChatRow(1, 1), historyChatRow(2, 2)
	old.ID, current.ID = "old-unread", "new-unread"
	old.State, current.State = "received", "received"
	old.ReadAt, current.ReadAt = nil, nil
	h, _ := historyChatHarness(t, current, old)
	seen := map[int64]bool{}
	operations := map[string]bool{}
	for range 2 {
		req := h.take(protocol.TypeOperatorContact)
		chatEqual(t, "display emits read", req.ContactOp, protocol.ContactOpRead)
		chatEqual(t, "audited operation ID", req.MessageID != "", true)
		if operations[req.MessageID] {
			t.Fatal("different epoch groups reused one operation ID")
		}
		operations[req.MessageID] = true
		if req.ContactEpoch != 1 && req.ContactEpoch != 2 {
			t.Fatalf("read omitted displayed pairing: %+v", req)
		}
		want := map[int64]string{1: old.ID, 2: current.ID}[req.ContactEpoch]
		chatEqual(t, "one pairing per read", slices.Equal(req.ContactRead, []string{want}), true)
		if seen[req.ContactEpoch] {
			t.Fatal("same pairing read twice while other remained unread")
		}
		seen[req.ContactEpoch] = true
		contactReadReply(h, req)
	}
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "both displayed pairings read", len(seen), 2)
}

func TestContactHistoryChatAcceptanceNumberedAckCarriesDisplayedEpoch(t *testing.T) {
	old, current := historyChatRow(1, 1), historyChatRow(2, 2)
	old.AckState, current.AckState = protocol.AckNeeded, protocol.AckNeeded
	h, key := historyChatHarness(t, current, old)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/ack #1")
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "private ACK", req.ContactOp, protocol.ContactOpAck)
	chatEqual(t, "selected contact", req.ContactKey, key)
	chatEqual(t, "old displayed pairing", req.ContactEpoch, int64(1))
	chatEqual(t, "selected reused wire ID", slices.Equal(req.ContactAck, []string{old.ID}), true)
}

func TestContactHistoryChatAcceptanceLegacyRowsKeepCurrentEpochDefault(t *testing.T) {
	row := contactLayoutRow("legacy-unread", 1)
	row.State, row.ReadAt = "received", nil
	h, _ := contactMessagesHarness(t, row)
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "legacy read", req.ContactOp, protocol.ContactOpRead)
	chatEqual(t, "legacy default epoch", req.ContactEpoch, int64(0))
	chatEqual(t, "legacy ID", slices.Equal(req.ContactRead, []string{row.ID}), true)
}
