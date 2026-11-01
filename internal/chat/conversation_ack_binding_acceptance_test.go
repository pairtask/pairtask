package chat_test

import (
	"encoding/json"
	"testing"

	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestChatConversationAudienceAckRetryKeepsOriginalBindings(t *testing.T) {
	h, _ := audienceAckHarness(t, true)
	audienceAckPrivate(h, "received", "alice")
	original := v2Send(h)
	h.reconnectTo(newChatScript(), "epoch")
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	chatEqual(t, "outcome remains unknown", h.m.State().Intent.State, chat.IntentUnknown)
	chatEqual(t, "unknown send acknowledges nothing", len(h.m.State().QueuedAcks), 0)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}})
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	retry := h.take(protocol.TypeOperatorSend)
	chatEqual(t, "retry reuses identity", retry.MessageID, original.MessageID)
	chatEqual(t, "retry preserves destination", retry.To, original.To)
	chatEqual(t, "retry does not invent reply", retry.InReplyTo, "")
	later := chatIncoming("after-reconnect", 5)
	later.From = "alice"
	h.reply(inbox, chatInbox(later))
	h.reply(history, chatHistory())
	receipt := h.take(protocol.TypeOperatorReceived)
	h.reply(receipt, chatReceipt(receipt.MessageIDs...))
	h.await(func() bool { return h.m.State().InFlight == 1 })
	audienceAckState(t, h, "alice-one", chat.AckNeeded)
	audienceAckAccept(h, retry, "alice")
	audienceAckFinish(h, "alice-one", "alice-two")
	audienceAckState(t, h, "after-reconnect", chat.AckNeeded)
	// A duplicate confirmation cannot consume the same batch twice.
	h.reply(retry, protocol.Message{Type: protocol.TypeAck, MessageID: retry.MessageID, Stage: protocol.StageAccepted, Seq: 10})
	h.event(protocol.EventPresenceChanged, "epoch", 3)
	h.await(func() bool { return h.m.State().Revision == 3 })
	chatEqual(t, "duplicate result dispatches nothing", h.m.State().InFlight, 0)
}

func TestChatConversationAudienceAckContentConflictSuspends(t *testing.T) {
	h, _ := replyAckHarness(t, true)
	audienceAckPrivate(h, "received", "author")
	sent := v2Send(h)
	h.event(protocol.EventConversationChanged, "epoch", 2)
	request := h.take(protocol.TypeOperatorHistory)
	changed := chatIncoming("target", 1)
	changed.Payload = json.RawMessage(`{"text":"changed after dispatch"}`)
	page := chatHistory(changed)
	page.History.Watermark.Revision = 2
	h.reply(request, page)
	h.await(func() bool { return h.m.State().Entries[0].Conflict })
	audienceAckAccept(h, sent, "author")
	replyAckSuspended(t, h, "content changed")
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func TestChatConversationAudienceAckEpochChangeSuspends(t *testing.T) {
	h, _ := replyAckHarness(t, true)
	audienceAckPrivate(h, "received", "author")
	sent := v2Send(h)
	h.event(protocol.EventResyncRequired, "next-epoch", 1)
	h.take(protocol.TypeOperatorSnapshot)
	h.reply(sent, protocol.Message{Type: protocol.TypeAck, MessageID: sent.MessageID, Stage: protocol.StageAccepted, Seq: 10,
		Recipients: []protocol.Recipient{{AgentID: "author", SessionID: "accepted-author"}}})
	h.await(func() bool { return h.m.State().Intent.State == chat.IntentAccepted })
	replyAckSuspended(t, h, "broker restarted")
	chatEqual(t, "old target not acknowledged", h.m.State().Entries[0].OperatorStage == protocol.StageAgentAcknowledged, false)
}

func TestChatConversationAudienceAckDoesNotDuplicateInflightOrCompletedAck(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "in flight", true: "completed"}[completed], func(t *testing.T) {
			h, _ := audienceAckHarness(t, true)
			v2Command(h, "/ack #1")
			explicit := h.take(protocol.TypeOperatorAck)
			if completed {
				h.reply(explicit, protocol.Message{Type: protocol.TypeAck, MessageID: explicit.MessageID, Stage: protocol.StageAgentAcknowledged})
				h.await(func() bool { return h.m.State().InFlight == 0 })
			}
			audienceAckPrivate(h, "received", "alice")
			audienceAckAccept(h, v2Send(h), "alice")
			if !completed {
				h.reply(explicit, protocol.Message{Type: protocol.TypeAck, MessageID: explicit.MessageID, Stage: protocol.StageAgentAcknowledged})
			}
			audienceAckFinish(h, "alice-two")
			audienceAckState(t, h, "alice-one", chat.AckDone)
			chatEqual(t, "existing ack not duplicated or canceled", len(h.m.State().QueuedAcks), 0)
		})
	}
}
