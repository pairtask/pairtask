package chat_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPrivateConversationAcceptancePublicArrivalStaysInAll(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		name := "selected direct view"
		if hidden {
			name = "hidden direct view"
		}
		t.Run(name, func(t *testing.T) {
			h := conversationHarness(t, "alice", "bob")
			_ = h.m.View()
			conversationOpen(h, "alice")
			if hidden {
				v2Command(h, "/close")
				conversationOpen(h, "bob")
			}
			msg := chatIncoming("public-arrival", 8)
			msg.From, msg.To = "alice", protocol.Broadcast
			deliverPrivateViewArrival(h, msg)
			if strings.Contains(ansi.Strip(h.m.View().Content), "body public-arrival") {
				t.Fatal("public arrival appeared in a direct conversation")
			}
			chatEqual(t, "public arrival has no direct badge", conversationBadge(t, h, "alice", false), 0)
			chatEqual(t, "public arrival remains unread in All", conversationBadge(t, h, "", true), 1)
			if hidden {
				chatEqual(t, "public arrival leaves direct view hidden", h.m.State().HiddenViews, 1)
				chatEqual(t, "public arrival keeps selection", h.m.State().Selected, "bob")
			}
			v2Command(h, "/chat all")
			v3Contains(t, ansi.Strip(h.m.View().Content), "body public-arrival")
			chatEqual(t, "viewing All clears public unread", h.m.State().Unread, 0)
			audienceAckState(t, h, msg.MessageID, chat.AckNeeded)
			chatEqual(t, "filtering never acknowledges", len(h.m.State().QueuedAcks), 0)
		})
	}
}

func deliverPrivateViewArrival(h *chatHarness, msg protocol.Message) {
	h.t.Helper()
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
		Kind: protocol.EventConversationChanged, Direction: "received",
		Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2},
	}}
	h.reply(h.take(protocol.TypeOperatorReceive), chatInbox(msg))
	receipt := h.take(protocol.TypeOperatorReceived)
	h.reply(receipt, chatReceipt(msg.MessageID))
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func TestPrivateConversationAcceptanceSharedSendsDoNotReopenDirectViews(t *testing.T) {
	for _, group := range []bool{false, true} {
		name := "public send"
		if group {
			name = "group send"
		}
		t.Run(name, func(t *testing.T) {
			h := conversationHarness(t, "alice", "bob")
			conversationOpen(h, "alice")
			v2Command(h, "/close")
			conversationOpen(h, "bob")
			msg := chatIncoming("shared-send", 8)
			msg.From, msg.To, msg.Origin = protocol.OperatorID, protocol.Broadcast, protocol.OriginOperator
			if group {
				msg.To, msg.Group = "group:shared-send", []string{"alice", "bob"}
			}
			page := chatHistory(msg)
			page.History.Entries[0].Direction = "sent"
			page.History.Entries[0].Recipients = []protocol.RecipientStage{
				{AgentID: "alice", Stage: protocol.StageAccepted},
				{AgentID: "bob", Stage: protocol.StageAccepted},
			}
			page.History.Watermark.Revision = 2
			h.event(protocol.EventConversationChanged, "epoch", 2)
			h.reply(h.take(protocol.TypeOperatorHistory), page)
			h.await(func() bool { return h.m.State().InFlight == 0 })
			if strings.Contains(ansi.Strip(h.m.View().Content), "body shared-send") {
				t.Fatal("shared send appeared in a direct conversation")
			}
			chatEqual(t, "shared send leaves direct view hidden", h.m.State().HiddenViews, 1)
			chatEqual(t, "shared send has no direct badge", conversationBadge(t, h, "alice", false), 0)
			chatEqual(t, "shared send keeps selection", h.m.State().Selected, "bob")
			if group {
				v2Command(h, "/chat @alice @bob")
				v3Contains(t, ansi.Strip(h.m.View().Content), "body shared-send")
			}
			v2Command(h, "/chat all")
			v3Contains(t, ansi.Strip(h.m.View().Content), "body shared-send")
		})
	}
}
