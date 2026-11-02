package chat_test

import (
	"encoding/json"
	"testing"

	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func audienceAckHarness(t *testing.T, confirmed bool) (*chatHarness, protocol.Message) {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	h.reply(history, chatHistory())
	var messages []protocol.Message
	for i, author := range []string{"alice", "alice", "bob", "carol"} {
		msg := chatIncoming([]string{"alice-one", "alice-two", "bob-one", "carol-one"}[i], uint64(i+1))
		msg.From = author
		messages = append(messages, msg)
	}
	h.reply(inbox, chatInbox(messages...))
	receipt := h.take(protocol.TypeOperatorReceived)
	if confirmed {
		h.reply(receipt, chatReceipt(receipt.MessageIDs...))
		h.await(func() bool { return h.m.State().InFlight == 0 })
	} else {
		h.await(func() bool { return h.m.State().InFlight == 1 })
	}
	return h, receipt
}

func audienceAckPrivate(h *chatHarness, body string, ids ...string) {
	h.t.Helper()
	encoded, err := json.Marshal(ids)
	if err != nil {
		h.t.Fatal(err)
	}
	v2Command(h, "/to "+string(encoded))
	v2Type(h, body)
}

func audienceAckAccept(h *chatHarness, sent protocol.Message, ids ...string) {
	h.t.Helper()
	var accepted []protocol.Recipient
	for _, id := range ids {
		accepted = append(accepted, protocol.Recipient{AgentID: id, SessionID: "accepted-" + id})
	}
	h.reply(sent, protocol.Message{Type: protocol.TypeAck, MessageID: sent.MessageID, Stage: protocol.StageAccepted, Seq: 10, Recipients: accepted})
	h.await(func() bool { return h.m.State().Intent.State == chat.IntentAccepted })
	replyAckFinishHistory(h)
}

func audienceAckFinish(h *chatHarness, ids ...string) {
	h.t.Helper()
	pending := map[string]bool{}
	for _, id := range ids {
		pending[id] = true
	}
	for range ids {
		request := h.take(protocol.TypeOperatorAck)
		if !pending[request.MessageID] {
			h.t.Fatalf("unexpected or duplicate acknowledgment %q", request.MessageID)
		}
		delete(pending, request.MessageID)
		h.reply(request, protocol.Message{Type: protocol.TypeAck, MessageID: request.MessageID, Stage: protocol.StageAgentAcknowledged})
	}
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func audienceAckState(t *testing.T, h *chatHarness, id, want string) {
	t.Helper()
	for _, entry := range h.m.State().Entries {
		if entry.MessageID == id {
			chatEqual(t, "acknowledgment of "+id, entry.Ack, want)
			return
		}
	}
	t.Fatalf("missing entry %q", id)
}

func TestChatConversationAudienceAckOnlyAcceptedGroupRecipients(t *testing.T) {
	h, _ := audienceAckHarness(t, true)
	audienceAckPrivate(h, "received", "alice", "bob")
	sent := v2Send(h)
	chatEqual(t, "plain group send invents no correlation", sent.InReplyTo, "")
	for _, id := range []string{"alice-one", "alice-two", "bob-one"} {
		audienceAckState(t, h, id, chat.AckNeeded)
	}
	audienceAckAccept(h, sent, "alice") // Bob was not accepted by the broker.
	audienceAckFinish(h, "alice-one", "alice-two")
	audienceAckState(t, h, "bob-one", chat.AckNeeded)
	audienceAckState(t, h, "carol-one", chat.AckNeeded)
	chatEqual(t, "automatic group acknowledgments stay quiet", h.m.State().Status, "")
}

func TestChatConversationAudienceAckNeverUsesBroadcastRecipients(t *testing.T) {
	for _, draft := range []string{"broadcast update", "@alice public assignment"} {
		t.Run(draft, func(t *testing.T) {
			h, _ := audienceAckHarness(t, true)
			v2Type(h, draft)
			sent := v2Send(h)
			chatEqual(t, "public post retains broadcast", sent.To, "*")
			audienceAckAccept(h, sent, "alice", "bob", "carol")
			h.await(func() bool { return h.m.State().InFlight == 0 })
			for _, id := range []string{"alice-one", "alice-two", "bob-one", "carol-one"} {
				audienceAckState(t, h, id, chat.AckNeeded)
			}
			chatEqual(t, "broadcast queues nothing even when tagged", len(h.m.State().QueuedAcks), 0)
		})
	}
}

func TestChatConversationAudienceAckExplicitReplyKeepsOnlyItsTarget(t *testing.T) {
	h, _ := audienceAckHarness(t, true)
	v2Command(h, "/reply #1")
	v2Type(h, "answer to the older message")
	sent := v2Send(h)
	chatEqual(t, "explicit reply correlation", sent.InReplyTo, "alice-one")
	audienceAckAccept(h, sent, "alice")
	audienceAckFinish(h, "alice-one")
	audienceAckState(t, h, "alice-two", chat.AckNeeded)
	audienceAckState(t, h, "bob-one", chat.AckNeeded)
}

func TestChatConversationAudienceAckWaitsForReceiptAndPreservesExplicitQueue(t *testing.T) {
	h, receipt := audienceAckHarness(t, false)
	v2Command(h, "/ack #1")
	audienceAckPrivate(h, "received both", "alice")
	audienceAckAccept(h, v2Send(h), "alice")
	h.await(func() bool { return h.m.State().InFlight == 1 }) // Receipt remains outstanding.
	chatEqual(t, "explicit queue not canceled and second target queued", len(h.m.State().QueuedAcks), 2)
	audienceAckState(t, h, "alice-one", chat.AckQueued)
	audienceAckState(t, h, "alice-two", chat.AckQueued)
	h.reply(receipt, chatReceipt(receipt.MessageIDs...))
	audienceAckFinish(h, "alice-one", "alice-two")
	audienceAckState(t, h, "bob-one", chat.AckNeeded)
}

func TestChatConversationAudienceAckRejectedSendLeavesEveryTargetPending(t *testing.T) {
	h, _ := audienceAckHarness(t, true)
	audienceAckPrivate(h, "received", "alice", "bob")
	sent := v2Send(h)
	h.reply(sent, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "refused"})
	h.await(func() bool { return h.m.State().Intent.State == chat.IntentRejected })
	chatEqual(t, "rejection does not queue", len(h.m.State().QueuedAcks), 0)
	chatEqual(t, "rejection does not dispatch", h.m.State().InFlight, 0)
	for _, id := range []string{"alice-one", "alice-two", "bob-one", "carol-one"} {
		audienceAckState(t, h, id, chat.AckNeeded)
	}
}

func TestChatConversationAudienceAckExcludesArrivalsAfterDispatch(t *testing.T) {
	h, _ := audienceAckHarness(t, true)
	audienceAckPrivate(h, "received", "alice")
	sent := v2Send(h)
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
		Kind: protocol.EventConversationChanged, Direction: "received", Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2},
	}}
	incoming := h.take(protocol.TypeOperatorReceive)
	later := chatIncoming("alice-later", 5)
	later.From = "alice"
	h.reply(incoming, chatInbox(later))
	receipt := h.take(protocol.TypeOperatorReceived)
	h.reply(receipt, chatReceipt(receipt.MessageIDs...))
	h.await(func() bool { return h.m.State().InFlight == 1 })
	audienceAckAccept(h, sent, "alice")
	audienceAckFinish(h, "alice-one", "alice-two")
	audienceAckState(t, h, "alice-later", chat.AckNeeded)
}

func TestChatConversationAudienceAckExcludesHistoryOnlyAtDispatch(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	historical := chatIncoming("history-only", 1)
	historical.From = "alice"
	h.reply(history, chatHistory(historical))
	h.await(func() bool { return len(h.m.State().Entries) == 1 })
	audienceAckPrivate(h, "new message", "alice")
	sent := v2Send(h)
	// Adoption occurs only after dispatch. Seeing it in history must not
	// retroactively add it to the frozen audience acknowledgment batch.
	h.reply(inbox, chatInbox(historical))
	receipt := h.take(protocol.TypeOperatorReceived)
	h.reply(receipt, chatReceipt(receipt.MessageIDs...))
	h.await(func() bool { return h.m.State().InFlight == 1 })
	audienceAckAccept(h, sent, "alice")
	audienceAckState(t, h, "history-only", chat.AckNeeded)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "history is not receipt adoption", len(h.m.State().QueuedAcks), 0)
}
