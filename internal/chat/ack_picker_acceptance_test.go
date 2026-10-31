package chat_test

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func ackPickerHarness(t *testing.T, confirmed bool) *chatHarness {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 120, Height: 24})
	inbox, history := h.baseline()
	h.reply(history, chatHistory())
	h.reply(inbox, chatInbox(chatIncoming("first", 1), chatIncoming("second", 2), chatIncoming("third", 3)))
	receipt := h.take(protocol.TypeOperatorReceived)
	if confirmed {
		h.reply(receipt, chatReceipt("first", "second", "third"))
		h.await(func() bool { return h.m.State().InFlight == 0 })
	}
	return h
}

func ackPickerItems(t *testing.T, h *chatHarness, want ...string) {
	t.Helper()
	p := h.m.State().Picker
	if p == nil || p.Kind != "argument" {
		t.Fatalf("ack picker missing: %+v", p)
	}
	if !reflect.DeepEqual(p.Items, want) {
		t.Fatalf("ack picker choices = %v, want %v", p.Items, want)
	}
}

func TestChatAckPickerOpeningAndEscapePreserveDraftAndReceipts(t *testing.T) {
	h := ackPickerHarness(t, true)
	v2Type(h, "unsent draft")
	v2Command(h, "/ack")
	ackPickerItems(t, h, "all", "from", "#3", "#2", "#1")
	chatEqual(t, "all initially selected", h.m.State().Picker.Selected, 0)
	chatEqual(t, "opening picker sends no acknowledgment", h.m.State().InFlight, 0)
	chatEqual(t, "opening picker queues no acknowledgment", len(h.m.State().QueuedAcks), 0)
	h.input(chatKey(tea.KeyDown, 0))
	ackPickerItems(t, h, "all", "from", "#3", "#2", "#1")
	chatEqual(t, "navigation cannot acknowledge", h.m.State().InFlight, 0)
	h.input(chatKey(tea.KeyEscape, 0))
	chatEqual(t, "Escape closes picker", h.m.State().Picker, nil)
	chatEqual(t, "Escape preserves draft", h.m.State().Draft, "unsent draft")
}

func TestChatAckPickerEnterAcknowledgesOnlySelectedMessage(t *testing.T) {
	h := ackPickerHarness(t, true)
	v2Type(h, "kept draft")
	v2Command(h, "/ack")
	ackPickerItems(t, h, "all", "from", "#3", "#2", "#1")
	for i := 0; i < 3; i++ {
		h.input(chatKey(tea.KeyDown, 0))
	}
	chatEqual(t, "down selects older message", h.m.State().Picker.Selected, 3)
	h.input(chatKey(tea.KeyEnter, 0))
	req := h.take(protocol.TypeOperatorAck)
	chatEqual(t, "selected message acknowledged", req.MessageID, "second")
	h.reply(req, protocol.Message{Type: protocol.TypeAck, MessageID: "second", Stage: protocol.StageAgentAcknowledged})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "picker closes on selection", h.m.State().Picker, nil)
	chatEqual(t, "selection preserves draft", h.m.State().Draft, "kept draft")
	v2Command(h, "/ack")
	ackPickerItems(t, h, "all", "from", "#3", "#1")
}

func TestChatAckPickerExcludesAcknowledgedAndInFlightMessages(t *testing.T) {
	h := ackPickerHarness(t, true)
	v2Command(h, "/ack #1")
	first := h.take(protocol.TypeOperatorAck)
	h.reply(first, protocol.Message{Type: protocol.TypeAck, MessageID: "first", Stage: protocol.StageAgentAcknowledged})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/ack #2")
	h.take(protocol.TypeOperatorAck) // The second acknowledgment stays in flight.
	v2Command(h, "/ack")
	ackPickerItems(t, h, "all", "from", "#3")
	chatEqual(t, "picker does not duplicate pending acknowledgment", h.m.State().InFlight, 1)
}

func TestChatAckPickerKeepsReceiptBeforeAckAndExcludesQueuedRows(t *testing.T) {
	h := ackPickerHarness(t, false)
	v2Command(h, "/ack")
	ackPickerItems(t, h, "all", "from", "#3", "#2", "#1")
	h.input(chatKey(tea.KeyDown, 0))
	h.input(chatKey(tea.KeyDown, 0))
	h.input(chatKey(tea.KeyTab, 0))
	chatEqual(t, "Tab queues no acknowledgment", len(h.m.State().QueuedAcks), 0)
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "only receipt request in flight", h.m.State().InFlight, 1)
	chatEqual(t, "selected acknowledgment queued", len(h.m.State().QueuedAcks), 1)
	chatEqual(t, "queued selected row", h.m.State().QueuedAcks[0].MessageID, "third")
	v2Command(h, "/ack")
	ackPickerItems(t, h, "all", "from", "#2", "#1")
}

func TestChatAckPickerEmptyInboxMakesNoRequest(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	v2Command(h, "/ack")
	chatEqual(t, "empty picker is not opened", h.m.State().Picker, nil)
	v3Contains(t, h.m.State().Status, "nothing to acknowledge")
	chatEqual(t, "empty picker makes no requests", h.m.State().InFlight, 0)
	if line := h.m.State().CommandLine; line == nil || strings.TrimSpace(line.Text) != "ack" {
		t.Fatal("empty picker discarded the command")
	}
}

func TestAckPickerAcceptanceAllIsExplicitAndLabelsGlobalScope(t *testing.T) {
	for _, view := range []string{"all", "direct", "room"} {
		t.Run(view, func(t *testing.T) {
			h := ackPickerHarness(t, true)
			if view == "direct" {
				v2Command(h, "/chat author")
			}
			if view == "room" {
				v2Command(h, "/room alpha")
				req := h.take(protocol.TypeOperatorRoom)
				h.reply(req, protocol.Message{Type: protocol.TypeOperatorRoomResult, RoomList: &protocol.RoomList{Rooms: []protocol.RoomInfo{{ID: "alpha"}}, Total: 1}})
				h.await(func() bool { return h.m.State().InFlight == 0 })
				v3Contains(t, h.m.State().Hint, "#alpha")
			}
			v2Command(h, "/ack")
			ackPickerItems(t, h, "all", "from", "#3", "#2", "#1")
			text := strings.ToLower(ansi.Strip(h.m.View().Content))
			v3Contains(t, text, "all conversations")
			v3Contains(t, text, "not approval")
			h.input(chatKey(tea.KeyTab, 0))
			chatEqual(t, "Tab only fills all", h.m.State().CommandLine.Text, "ack all ")
			chatEqual(t, "no status or mutation before submit", h.m.State().InFlight, 0)
			h.input(chatKey(tea.KeyEnter, 0))
			req := h.take(protocol.TypeOperatorInboxStatus)
			usage := bulkUsage(3)
			h.reply(req, protocol.Message{Type: protocol.TypeOperatorInboxStatus, Inbox: &usage})
			write := h.take(protocol.TypeOperatorAckMany)
			chatEqual(t, "all keeps global sender scope", write.AckSelection.From, "")
			chatEqual(t, "all bounded by observed head", write.AckSelection.UpperSeq, uint64(3))
		})
	}
}

func TestAckPickerAcceptanceFromOffersNamesWithTabPositions(t *testing.T) {
	h := commandCompleteHarness(t)
	v2Command(h, "/ack from")
	p := h.m.State().Picker
	if p == nil || !slices.Contains(p.Items, "alice") {
		t.Fatalf("agent choices missing: %+v", p)
	}
	position := 0
	for _, c := range h.m.State().Conversations {
		if c.ID == "alice" {
			position = c.Position
		}
	}
	if position == 0 {
		t.Fatal("alice has no displayed tab")
	}
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, fmt.Sprint(position))
	v3Contains(t, view, "alice")
	for i := 0; i < slices.Index(p.Items, "alice"); i++ {
		h.input(chatKey(tea.KeyDown, 0))
	}
	h.input(chatKey(tea.KeyTab, 0))
	chatEqual(t, "Tab inserts identity not tab number", h.m.State().CommandLine.Text, "ack from alice ")
	chatEqual(t, "choosing an agent changes no receipts", h.m.State().InFlight, 0)
}

func TestAckPickerAcceptanceMissingNumericSenderExplainsTabAndMessage(t *testing.T) {
	h := commandCompleteHarness(t)
	var agent string
	for _, c := range h.m.State().Conversations {
		if c.Position == 3 {
			agent = c.ID
		}
	}
	if agent == "" {
		t.Fatal("fixture requires agent tab 3")
	}
	v2Command(h, "/ack from #3")
	req := h.take(protocol.TypeOperatorInboxStatus)
	usage := bulkUsage(3) // Pending messages exist, but none are from an agent named #3.
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorInboxStatus, Inbox: &usage})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	status := h.m.State().Status
	for _, want := range []string{"#3", "tab 3", agent, "/ack from " + agent, "/ack #3"} {
		v3Contains(t, status, want)
	}
	chatEqual(t, "missing sender queues no acknowledgments", len(h.m.State().QueuedAcks), 0)
}

func TestAckPickerAcceptanceLiteralNumericAgentStillAcknowledges(t *testing.T) {
	h := ackPickerHarness(t, true)
	v2Command(h, "/ack from #3")
	req := h.take(protocol.TypeOperatorInboxStatus)
	usage := bulkUsage(1)
	usage.Senders = []protocol.SenderUsage{{AgentID: "#3", Unacknowledged: 1}}
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorInboxStatus, Inbox: &usage})
	write := h.take(protocol.TypeOperatorAckMany)
	chatEqual(t, "literal numeric agent identity retained", write.AckSelection.From, "#3")
}
