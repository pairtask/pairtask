package chat_test

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestChatInboxWarningLoadsUsageForPendingWorkAtStartup(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 180, Height: 24})
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{
		Watermark: protocol.Watermark{Epoch: "epoch", Revision: 1}, InboxPending: 12, InboxHeadSeq: 12,
	}})
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	status := h.take(protocol.TypeOperatorInboxStatus)
	usage := bulkUsage(12)
	usage.HeadSeq = 12
	h.reply(status, protocol.Message{Type: protocol.TypeOperatorInboxStatus, Inbox: &usage})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	row := footerRow(h)
	v3Contains(t, ansi.Strip(row), "12 need ack · /ack all")
	v3Contains(t, ansi.Strip(row), "12/16")
	formattingWarningStyle(t, v3StyleAt(t, row, "12/16"))
	chatEqual(t, "warning does not acknowledge", len(h.m.State().QueuedAcks), 0)
	v2FrameFits(t, h, 180, 24)
}

func TestChatInboxWarningRefusalIsVisibleAndRefreshesUsage(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
		Kind: protocol.EventSendRefused, AgentID: "author", Detail: "sender messages: 16/16 messages",
		Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2},
	}}
	status := h.take(protocol.TypeOperatorInboxStatus)
	v3Contains(t, h.m.State().Status, "author's message was refused")
	v3Contains(t, h.m.State().Status, "16/16")
	usage := bulkUsage(16)
	h.reply(status, protocol.Message{Type: protocol.TypeOperatorInboxStatus, Inbox: &usage})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, h.m.State().Status, "message was refused")
	chatEqual(t, "notice cannot submit a draft", h.m.State().Intent, nil)
	chatEqual(t, "notice cannot acknowledge", len(h.m.State().QueuedAcks), 0)
}

func TestChatBulkAckDoesNotUseUnrelatedEarlierStatusResponse(t *testing.T) {
	h := ackPickerHarness(t, true)
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
		Kind: protocol.EventSendRefused, AgentID: "author", Detail: "sender messages: 16/16 messages",
		Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2},
	}}
	earlier := h.take(protocol.TypeOperatorInboxStatus)
	v2Command(h, "/ack all")
	own := h.take(protocol.TypeOperatorInboxStatus)
	old := bulkUsage(2)
	old.HeadSeq, old.OldestUnacknowledgedSeq = 999, 998
	commands := h.holdResponse(earlier, protocol.Message{Type: protocol.TypeOperatorInboxStatus, Inbox: &old})
	chatEqual(t, "unrelated status cannot start a mutation", h.m.State().InFlight, 1)
	h.launch(commands)
	current := bulkUsage(3)
	h.reply(own, protocol.Message{Type: protocol.TypeOperatorInboxStatus, Inbox: &current})
	mutation := h.take(protocol.TypeOperatorAckMany)
	chatEqual(t, "bulk uses its own captured upper", mutation.AckSelection.UpperSeq, uint64(3))
	chatEqual(t, "bulk uses its own captured start", mutation.AckSelection.AfterSeq, uint64(0))
}

func TestChatInboxWarningRefreshesChangesThatArriveDuringStatusRead(t *testing.T) {
	h := ackPickerHarness(t, true)
	three := 3
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
		Kind: protocol.EventInboxChanged, Pending: &three, HeadSeq: 3,
		Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2},
	}}
	first := h.take(protocol.TypeOperatorInboxStatus)
	drain := h.take(protocol.TypeOperatorReceive)
	h.reply(drain, chatInbox())
	h.await(func() bool { return h.m.State().InFlight == 1 })
	two := 2
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
		Kind: protocol.EventInboxChanged, Pending: &two, HeadSeq: 3,
		Watermark: protocol.Watermark{Epoch: "epoch", Revision: 3},
	}}
	h.await(func() bool { return h.m.State().Revision == 3 })
	chatEqual(t, "in-flight refresh coalesces requests", h.m.State().InFlight, 1)
	old := bulkUsage(3)
	h.reply(first, protocol.Message{Type: protocol.TypeOperatorInboxStatus, Inbox: &old})
	again := h.take(protocol.TypeOperatorInboxStatus)
	current := bulkUsage(2)
	h.reply(again, protocol.Message{Type: protocol.TypeOperatorInboxStatus, Inbox: &current})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "change during refresh is not forgotten", h.m.State().InboxUsage.Unacknowledged, 2)
}
