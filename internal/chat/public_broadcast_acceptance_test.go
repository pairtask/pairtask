package chat_test

import (
	"testing"

	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func publicChatMessage(id string, seq uint64) protocol.Message {
	m := chatIncoming(id, seq)
	m.To = "*"
	return m
}

func TestPublicBroadcastAcceptanceChatHistoryAndInboxDeduplicate(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	m := publicChatMessage("public", 1)
	h.reply(history, chatHistory(m))
	h.await(func() bool { return len(h.m.State().Entries) == 1 })
	chatEqual(t, "history does not receipt", h.m.State().Entries[0].Receipt, chat.ReceiptNone)
	h.reply(inbox, chatInbox(m))
	receipt := h.take(protocol.TypeOperatorReceived)
	chatEqual(t, "one receipt target", len(receipt.MessageIDs), 1)
	chatEqual(t, "public receipt identity", receipt.MessageIDs[0], "public")
	h.reply(receipt, chatReceipt("public"))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	s := h.m.State()
	chatEqual(t, "deduplicated entry", len(s.Entries), 1)
	chatEqual(t, "body preserved", s.Entries[0].Body, "body public")
	chatEqual(t, "still needs explicit acknowledgment", s.Entries[0].Ack, chat.AckNeeded)
	v3Contains(t, h.m.View().Content, "author")
}

func TestPublicBroadcastAcceptanceChatReplyPreservesAudienceAndAckTarget(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	h.reply(history, chatHistory())
	h.reply(inbox, chatInbox(publicChatMessage("target", 1), publicChatMessage("other", 2)))
	receipt := h.take(protocol.TypeOperatorReceived)
	h.reply(receipt, chatReceipt("target", "other"))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	sent := replyAckSend(h)
	chatEqual(t, "public reply audience", sent.To, "*")
	chatEqual(t, "original target", sent.InReplyTo, "target")
	chatEqual(t, "no premature ack", h.m.State().Entries[0].Ack, chat.AckNeeded)
	h.reply(sent, protocol.Message{Type: protocol.TypeAck, MessageID: sent.MessageID, Stage: protocol.StageAccepted, Seq: 3})
	ack := h.take(protocol.TypeOperatorAck)
	replyAckFinishHistory(h)
	replyAckConfirm(h, ack)
	chatEqual(t, "other public message remains pending", h.m.State().Entries[1].Ack, chat.AckNeeded)
}

func TestPublicBroadcastAcceptanceChatAdoptsBeforeReceipt(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	h.reply(history, chatHistory())
	commands := h.holdResponse(inbox, chatInbox(publicChatMessage("public", 1)))
	chatEqual(t, "entry adopted before write", len(h.m.State().Entries), 1)
	chatEqual(t, "complete body adopted", h.m.State().Entries[0].Body, "body public")
	h.launch(commands)
	receipt := h.take(protocol.TypeOperatorReceived)
	chatEqual(t, "only adopted target", receipt.MessageIDs[0], "public")
	chatEqual(t, "adoption never acknowledges", h.m.State().Entries[0].Ack, chat.AckNeeded)
}

func TestPublicBroadcastAcceptanceChatNewBroadcastDoesNotAcknowledgePending(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	h.reply(history, chatHistory())
	h.reply(inbox, chatInbox(publicChatMessage("target", 1)))
	receipt := h.take(protocol.TypeOperatorReceived)
	h.reply(receipt, chatReceipt("target"))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Type(h, "new public topic")
	sent := v2Send(h)
	chatEqual(t, "public destination", sent.To, "*")
	chatEqual(t, "no implicit reply", sent.InReplyTo, "")
	audienceAckAccept(h, sent, "author")
	h.await(func() bool { return h.m.State().InFlight == 0 })
	audienceAckState(t, h, "target", chat.AckNeeded)
	chatEqual(t, "no queued acknowledgment", len(h.m.State().QueuedAcks), 0)
}

func publicChatHistoryHarness(t *testing.T) *chatHarness {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	h.reply(history, chatHistory(publicChatMessage("target", 1)))
	h.reply(inbox, chatInbox())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h
}

func TestPublicBroadcastAcceptanceChatReplyFromAuthorViewMovesToAll(t *testing.T) {
	h := publicChatHistoryHarness(t)
	conversationOpen(h, "author")
	chatEqual(t, "reply begins in the private view", h.m.State().Selected, "author")
	sent := replyAckSend(h)
	chatEqual(t, "public reply displayed in All", h.m.State().SelectedAll, true)
	chatEqual(t, "public audience retained from author view", sent.To, "*")
	chatEqual(t, "parent retained", sent.InReplyTo, "target")
}

func TestPublicBroadcastAcceptanceChatReplyCannotOverwriteAllDraft(t *testing.T) {
	h := publicChatHistoryHarness(t)
	v2Type(h, "existing public draft")
	conversationOpen(h, "author")
	v2Command(h, "/reply #1")
	chatEqual(t, "occupied All prevents switch", h.m.State().Selected, "author")
	chatEqual(t, "no send dispatched", h.m.State().Intent, nil)
	v2Command(h, "/chat all")
	chatEqual(t, "original All draft retained", h.m.State().Draft, "existing public draft")
}

func TestPublicBroadcastAcceptanceChatReplyCannotPromotePrivateDraft(t *testing.T) {
	h := publicChatHistoryHarness(t)
	conversationOpen(h, "author")
	v2Type(h, "private draft")
	v2Command(h, "/reply #1")
	chatEqual(t, "private view retained", h.m.State().Selected, "author")
	chatEqual(t, "private text retained", h.m.State().Draft, "private draft")
	chatEqual(t, "no send dispatched", h.m.State().Intent, nil)
	v3Contains(t, h.m.State().Status, "already addressed")
}
