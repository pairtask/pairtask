package chat_test

import (
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func replyAckHarness(t *testing.T, confirmed bool) (*chatHarness, protocol.Message) {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	h.reply(history, chatHistory())
	h.reply(inbox, chatInbox(chatIncoming("target", 1)))
	receipt := h.take(protocol.TypeOperatorReceived)
	if confirmed {
		h.reply(receipt, chatReceipt("target"))
		h.await(func() bool { return h.m.State().InFlight == 0 })
	} else {
		h.await(func() bool { return h.m.State().InFlight == 1 })
	}
	return h, receipt
}

func replyAckSend(h *chatHarness) protocol.Message {
	h.t.Helper()
	v2Command(h, "/reply #1")
	v2Type(h, "reply body")
	return v2Send(h)
}

func replyAckAccept(h *chatHarness, sent protocol.Message) {
	h.t.Helper()
	h.reply(sent, protocol.Message{Type: protocol.TypeAck, MessageID: sent.MessageID, Stage: protocol.StageAccepted, Seq: 2})
	h.await(func() bool { return h.m.State().Intent.State == chat.IntentAccepted })
}

func replyAckFinishHistory(h *chatHarness) {
	h.t.Helper()
	history := h.take(protocol.TypeOperatorHistory)
	h.reply(history, chatHistory())
}

func replyAckConfirm(h *chatHarness, ack protocol.Message) {
	h.t.Helper()
	chatEqual(h.t, "ack targets the original message", ack.MessageID, "target")
	h.reply(ack, protocol.Message{Type: protocol.TypeAck, MessageID: "target", Stage: protocol.StageAgentAcknowledged})
	h.await(func() bool { return h.m.State().Entries[0].OperatorStage == protocol.StageAgentAcknowledged })
}

func TestChatReplyAckWaitsForSendAcceptanceAndStaysQuiet(t *testing.T) {
	h, _ := replyAckHarness(t, true)
	sent := replyAckSend(h)
	chatEqual(t, "reply correlation", sent.InReplyTo, "target")
	chatEqual(t, "no ack before acceptance", h.m.State().Entries[0].Ack, chat.AckNeeded)
	chatEqual(t, "only send pending", h.m.State().InFlight, 1)
	v2Type(h, "newer unrelated draft")
	replyAckAccept(h, sent)
	chatEqual(t, "accepted reply requests acknowledgment", h.m.State().Entries[0].Ack, chat.AckQueued)
	ack := h.take(protocol.TypeOperatorAck)
	chatEqual(t, "automatic dispatch stays quiet", h.m.State().Status, "")
	replyAckFinishHistory(h)
	replyAckConfirm(h, ack)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "automatic confirmation stays quiet", h.m.State().Status, "")
	chatEqual(t, "late acceptance preserves newer draft", h.m.State().Draft, "newer unrelated draft")
}

func TestChatReplyAckQueuesUntilReceiptConfirmed(t *testing.T) {
	h, receipt := replyAckHarness(t, false)
	sent := replyAckSend(h)
	replyAckAccept(h, sent)
	chatEqual(t, "one queued acknowledgment", len(h.m.State().QueuedAcks), 1)
	chatEqual(t, "queued target", h.m.State().QueuedAcks[0].MessageID, "target")
	chatEqual(t, "automatic queue stays quiet", h.m.State().Status, "")
	replyAckFinishHistory(h)
	h.await(func() bool { return h.m.State().InFlight == 1 })
	chatEqual(t, "receipt still pending", h.m.State().Entries[0].Receipt, chat.ReceiptPending)
	h.reply(receipt, chatReceipt("target"))
	ack := h.take(protocol.TypeOperatorAck)
	replyAckConfirm(h, ack)
	chatEqual(t, "queued automatic confirmation stays quiet", h.m.State().Status, "")
}

func TestChatReplyAckDoesNotCancelExistingExplicitQueue(t *testing.T) {
	h, receipt := replyAckHarness(t, false)
	v2Command(h, "/ack #1")
	chatEqual(t, "explicit queue exists", len(h.m.State().QueuedAcks), 1)
	sent := replyAckSend(h)
	replyAckAccept(h, sent)
	chatEqual(t, "reply preserves explicit queue", len(h.m.State().QueuedAcks), 1)
	replyAckFinishHistory(h)
	h.reply(receipt, chatReceipt("target"))
	ack := h.take(protocol.TypeOperatorAck)
	replyAckConfirm(h, ack)
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func TestChatReplyAckDoesNotDuplicateExplicitAckInFlight(t *testing.T) {
	h, _ := replyAckHarness(t, true)
	v2Command(h, "/ack #1")
	ack := h.take(protocol.TypeOperatorAck)
	sent := replyAckSend(h)
	replyAckAccept(h, sent)
	replyAckFinishHistory(h)
	h.await(func() bool { return h.m.State().InFlight == 1 })
	replyAckConfirm(h, ack)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "no second acknowledgment queued", len(h.m.State().QueuedAcks), 0)
}

func TestChatReplyAckDoesNotRepeatAcknowledgedTarget(t *testing.T) {
	h, _ := replyAckHarness(t, true)
	v2Command(h, "/ack #1")
	replyAckConfirm(h, h.take(protocol.TypeOperatorAck))
	sent := replyAckSend(h)
	replyAckAccept(h, sent)
	replyAckFinishHistory(h)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "target remains acknowledged", h.m.State().Entries[0].Ack, chat.AckDone)
	chatEqual(t, "nothing requeued", len(h.m.State().QueuedAcks), 0)
}

func TestChatReplyAckRejectedSendNeverAcknowledges(t *testing.T) {
	h, _ := replyAckHarness(t, true)
	sent := replyAckSend(h)
	h.reply(sent, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "send rejected"})
	h.await(func() bool { return h.m.State().Intent.State == chat.IntentRejected })
	chatEqual(t, "rejected reply does not ack", h.m.State().Entries[0].Ack, chat.AckNeeded)
	chatEqual(t, "no queued ack", len(h.m.State().QueuedAcks), 0)
	chatEqual(t, "no ack request", h.m.State().InFlight, 0)
}

// Chat C extends plain direct sends to acknowledge their dispatch-time audience.
func TestChatConversationPlainDirectSendAcknowledgesWithoutCorrelation(t *testing.T) {
	h, _ := replyAckHarness(t, true)
	audienceAckPrivate(h, "plain message", "author")
	sent := v2Send(h)
	chatEqual(t, "plain send has no correlation", sent.InReplyTo, "")
	audienceAckAccept(h, sent, "author")
	audienceAckFinish(h, "target")
	chatEqual(t, "plain accepted send acknowledges its audience", h.m.State().Entries[0].Ack, chat.AckDone)
	chatEqual(t, "automatic acknowledgment stays quiet", h.m.State().Status, "")
}

func TestChatReplyAckUnknownSendWaitsForAcceptedRetry(t *testing.T) {
	h, _ := replyAckHarness(t, true)
	original := replyAckSend(h)
	h.reconnectTo(newChatScript(), "epoch")
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	chatEqual(t, "send outcome unknown", h.m.State().Intent.State, chat.IntentUnknown)
	chatEqual(t, "unknown outcome never queues ack", len(h.m.State().QueuedAcks), 0)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}})
	inbox := h.take(protocol.TypeOperatorReceive)
	history := h.take(protocol.TypeOperatorHistory)
	retry := h.take(protocol.TypeOperatorSend)
	chatEqual(t, "retry keeps send identity", retry.MessageID, original.MessageID)
	chatEqual(t, "retry keeps original reply target", retry.InReplyTo, "target")
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory(chatIncoming("target", 1)))
	h.await(func() bool { return h.m.State().InFlight == 1 })
	chatEqual(t, "retry submission alone does not ack", h.m.State().Entries[0].Ack, chat.AckNeeded)
	replyAckAccept(h, retry)
	chatEqual(t, "late acceptance acknowledges", h.m.State().Entries[0].Ack, chat.AckQueued)
	replyAckFinishHistory(h)
	replyAckConfirm(h, h.take(protocol.TypeOperatorAck))
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func TestChatReplyAckFailureDoesNotSilenceLaterExplicitAck(t *testing.T) {
	h, _ := replyAckHarness(t, true)
	replyAckAccept(h, replyAckSend(h))
	chatEqual(t, "automatic acknowledgment pending", h.m.State().Entries[0].Ack, chat.AckQueued)
	automatic := h.take(protocol.TypeOperatorAck)
	replyAckFinishHistory(h)
	h.reply(automatic, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "ack unavailable"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "failed automatic ack not confirmed", h.m.State().Entries[0].Ack, chat.AckNeeded)
	v2Command(h, "/ack #1")
	replyAckConfirm(h, h.take(protocol.TypeOperatorAck))
	if !strings.Contains(h.m.State().Status, "acknowledged by operator") {
		t.Fatalf("explicit acknowledgment lost its confirmation: %q", h.m.State().Status)
	}
}

func TestChatReplyAckDisconnectDoesNotSilenceLaterExplicitAck(t *testing.T) {
	h, _ := replyAckHarness(t, true)
	replyAckAccept(h, replyAckSend(h))
	chatEqual(t, "automatic acknowledgment pending", h.m.State().Entries[0].Ack, chat.AckQueued)
	h.take(protocol.TypeOperatorAck) // The connection drops without a confirmation.
	replyAckFinishHistory(h)
	h.await(func() bool { return h.m.State().InFlight == 1 })
	h.reconnectTo(newChatScript(), "epoch")
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}})
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory(chatIncoming("target", 1)))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/ack #1")
	replyAckConfirm(h, h.take(protocol.TypeOperatorAck))
	if !strings.Contains(h.m.State().Status, "acknowledged by operator") {
		t.Fatalf("explicit acknowledgment after disconnect lost its confirmation: %q", h.m.State().Status)
	}
}
