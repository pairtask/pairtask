package hub

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// Paired mentions (#278): an operator's ⇄ message may tag this side's agents
// and operator; they get it as an operator message naming the shared
// conversation, and a tagged agent's reply goes back to that conversation
// and on to the same audience there, as the store decides.

// errHomeAudience is a ⇄ send naming this side's agents badly.
var errHomeAudience = errors.New("contact_here names each of this side's agents once, and only agents this broker knows")

// checkNoConversation: only the broker says which shared conversation a
// message belongs to, or what became of a reply's peer copy.
func checkNoConversation(msg protocol.Message) *routingError {
	if msg.Conversation != nil || msg.PeerDelivery != nil {
		return &routingError{protocol.ErrMalformedFrame, "conversation and peer_delivery are set by the broker"}
	}
	return nil
}

// homeRecipients are the agents a ⇄ send tags here, each one known to this
// broker as operator messages need, and named once.
func (h *Hub) homeRecipients(ctx context.Context, clients map[string]*Client, here []string) ([]protocol.Recipient, error) {
	sorted := slices.Clone(here)
	slices.Sort(sorted)
	if len(slices.Compact(sorted)) != len(here) {
		return nil, errHomeAudience
	}
	recipients := make([]protocol.Recipient, 0, len(here))
	for _, id := range here {
		r, rerr := h.operatorMember(ctx, clients, id)
		if rerr != nil {
			return nil, errHomeAudience
		}
		recipients = append(recipients, r)
	}
	return recipients, nil
}

// homeRefusal is a ⇄ send naming this side's agents badly.
func homeRefusal(err error) (*routingError, bool) {
	if errors.Is(err, errHomeAudience) {
		return &routingError{protocol.ErrMalformedFrame, err.Error()}, true
	}
	return nil, false
}

// sendsHere: the ⇄ send also tags this side's agents or operator.
func sendsHere(msg protocol.Message) bool {
	return len(msg.ContactHere) > 0 || msg.ContactOperatorHere
}

// homeSend is a ⇄ send's message here, as stored: its order and time.
type homeSend struct {
	msg        protocol.Message
	seq        uint64
	at         time.Time
	recipients []protocol.Recipient
}

// deliverHere hands the ⇄ send's message here to the tagged agents online,
// with its reply assigned and its conversation named as stored; replay
// delivers what this misses.
func (h *Hub) deliverHere(ctx context.Context, clients map[string]*Client, s homeSend) {
	b, found, err := h.store.PairedThread(ctx, s.msg.MessageID)
	if err != nil || !found {
		return
	}
	ts := s.at.UTC()
	out := protocol.Message{Type: protocol.TypeMsg, Seq: s.seq, TS: &ts, From: protocol.OperatorID, To: "group:" + s.msg.MessageID,
		Origin: protocol.OriginOperator, Payload: homePayload(s.msg.ContactText), MessageID: s.msg.MessageID, AckRequested: true,
		Durable: true, Group: homeTargets(s.msg),
		Conversation: &protocol.ConversationRef{Kind: protocol.ConversationContact, Contact: b.Contact, Root: b.RootLocalID, PeerAudience: b.Audience}}
	for _, r := range s.recipients {
		if c := sessionOwner(clients, r); c != nil {
			h.enqueue(clients, c, recipientFrame(out, r.AgentID, s.msg.ContactHere))
		}
	}
}

// contactSendRow is what the store keeps of a ⇄ send: with the order its
// message here takes, when it tags someone here, and the tagged agents as
// resolved for delivery, so the store keeps the sessions they go to.
func (h *Hub) contactSendRow(msg protocol.Message, auditID int64, home []protocol.Recipient) store.ContactSend {
	send := store.ContactSend{OperationID: msg.MessageID, Contact: msg.ContactKey, Text: msg.ContactText, To: peerRecipients(msg),
		AuditID: auditID, Now: h.contactNow(), Here: msg.ContactHere, OperatorHere: msg.ContactOperatorHere, Recipients: home}
	if sendsHere(msg) {
		send.Seq = h.seq + 1
	}
	return send
}

// sentHere announces and delivers a ⇄ send's message here, if it has one.
func (h *Hub) sentHere(ctx context.Context, clients map[string]*Client, s homeSend) {
	if !sendsHere(s.msg) {
		return
	}
	h.seq++
	h.emit(protocol.SessionEvent{Kind: protocol.EventConversationChanged, Seq: s.seq, MessageID: s.msg.MessageID, Direction: "sent"})
	h.deliverHere(ctx, clients, s)
}

// peerRecipients are the send's recipients there: canonical when it also
// tags here, so its frozen audience and the peer copy agree (#278).
func peerRecipients(msg protocol.Message) []string {
	if !sendsHere(msg) || len(msg.ContactTo) == 0 {
		return msg.ContactTo
	}
	to := slices.Clone(msg.ContactTo)
	slices.Sort(to)
	return slices.Compact(to)
}

// homeTargets are the send's tagged agents and operator here, canonical,
// as the store keeps them.
func homeTargets(msg protocol.Message) []string {
	targets := slices.Clone(msg.ContactHere)
	if msg.ContactOperatorHere {
		targets = append(targets, protocol.OperatorID)
	}
	slices.Sort(targets)
	return slices.Compact(targets)
}

// readableContact admits an operation on a confirmed contact, and a list of
// one no longer there whose shared conversations this side keeps: read
// only, the contact never recreated.
func (h *Hub) readableContact(ctx context.Context, msg protocol.Message) *routingError {
	rerr := h.confirmedContact(ctx, msg.ContactKey)
	if rerr == nil || msg.ContactOp != protocol.ContactOpList {
		return rerr
	}
	if kept, err := h.store.KeepsConversations(ctx, msg.ContactKey); err == nil && kept {
		return nil
	}
	return rerr
}

// inSharedConversation: the message replied to belongs to a paired
// workspace's conversation.
func (h *Hub) inSharedConversation(ctx context.Context, id string) bool {
	_, found, err := h.store.PairedThread(ctx, id)
	return err == nil && found
}

// routePaired takes an agent's reply in a shared conversation: kept there
// and, when the agent may, queued to the same audience on the other side.
// Anything else is routed as before.
func (h *Hub) routePaired(ctx context.Context, clients map[string]*Client, in Inbound) bool {
	msg := in.Msg
	if msg.InReplyTo == "" {
		return false
	}
	if _, found, err := h.store.PairedThread(ctx, msg.InReplyTo); err != nil || !found {
		return false
	}
	if h.store.ValidateOperatorReply(ctx, in.From.ID, rootOf(ctx, h.store, msg.InReplyTo)) != nil {
		return false // not a tagged agent's: refused as any other reply
	}
	msg.From = in.From.ID
	result, err := h.store.PairedReply(ctx, store.PairedReply{Msg: msg, Session: in.From.SessionID, Now: time.Now().UTC(), Seq: h.seq + 1})
	if err != nil {
		h.enqueue(clients, in.From, pairedRefusal(err).frame(msg.MessageID))
		return true
	}
	h.pairedReplied(clients, in, result)
	return true
}

// rootOf is the conversation root a reply's parent names, or the parent.
func rootOf(ctx context.Context, st *store.Store, parent string) string {
	if b, found, err := st.PairedThread(ctx, parent); err == nil && found {
		return b.RootLocalID
	}
	return parent
}

// pairedReplied answers the reply and announces what it changed.
func (h *Hub) pairedReplied(clients map[string]*Client, in Inbound, r store.PairedReplyResult) {
	if !r.Repeated {
		h.seq++
		h.emit(protocol.SessionEvent{Kind: protocol.EventConversationChanged, Seq: r.Seq, MessageID: in.Msg.MessageID, Direction: "received"})
	}
	delivery := r.PeerDelivery
	h.enqueue(clients, in.From, protocol.Message{Type: protocol.TypeAck, MessageID: in.Msg.MessageID, Stage: protocol.StageAccepted, Seq: r.Seq,
		Durable: true, Repeated: r.Repeated, PeerDelivery: &delivery})
	if delivery.State == protocol.PeerQueued {
		h.SyncMessages(r.Binding.Contact)
	}
	h.MessagesChanged(r.Binding.Contact) // a reply kept here only is in that workspace's history too
}

// pairedRefusal is what an agent hears of a reply the conversation refused.
func pairedRefusal(err error) *routingError {
	if errors.Is(err, store.ErrConversationConflict) {
		return &routingError{protocol.ErrMalformedFrame, err.Error()}
	}
	return &routingError{protocol.ErrInternal, "the shared conversation is unavailable"}
}

// homePayload is a ⇄ send's text as its message here carries it.
func homePayload(text string) json.RawMessage {
	payload, _ := json.Marshal(map[string]string{"text": text}) // a map of strings always encodes
	return payload
}
