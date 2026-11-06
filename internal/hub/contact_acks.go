package hub

import (
	"context"
	"errors"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// The operator's acknowledgment of a contact's messages (#287): private,
// audited, and announced so every operator view recounts. The contact is
// told nothing, and no message's read state or receipt changes.

// contactAck acknowledges the selected messages with its accepted audit,
// and answers how many and where the contact's acknowledgments stand.
func (h *Hub) contactAck(ctx context.Context, req operatorRequest, auditID int64) (protocol.Message, error) {
	msg := req.msg
	acked, summary, err := h.store.OperatorContactAck(ctx, store.ContactAck{OperationID: msg.MessageID, Contact: msg.ContactKey,
		Selector: store.AckSelector{IDs: msg.ContactAck, Upto: msg.ContactAckUpto}, AuditID: auditID, Now: h.contactNow(), Epoch: msg.ContactEpoch})
	if err != nil {
		return protocol.Message{}, err
	}
	if acked > 0 {
		h.MessagesChanged(msg.ContactKey)
	}
	return protocol.Message{Type: protocol.TypeOperatorContactResult, MessageID: msg.MessageID, ContactKey: msg.ContactKey,
		ContactOp: msg.ContactOp, ContactAcked: &acked, ContactAcks: &summary}, nil
}

// ackRefusal: a cursor past the newest message received.
func ackRefusal(err error) (*routingError, bool) {
	if errors.Is(err, store.ErrAckBound) {
		return &routingError{protocol.ErrMalformedFrame, err.Error()}, true
	}
	return nil, false
}

// ackSummary is the contact's acknowledgments for a view.
func (h *Hub) ackSummary(ctx context.Context, contact string) (*protocol.ContactAckSummary, error) {
	s, err := h.store.PeerMessages().AckSummary(ctx, contact)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ackView is one contact's acknowledgments in the snapshot: zero for a
// contact without messages.
func ackView(acks map[string]protocol.ContactAckSummary, contact string) *protocol.ContactAckSummary {
	s := acks[contact]
	return &s
}

// ackBound refuses a new acknowledgment's cursor past the newest message
// received in the pairing it names (#276), or a pairing the contact never
// had, before anything is audited or changed. Messages only arrive,
// so a cursor that passes here still holds when it is applied. A retry of
// one already stored is answered from it, even once the messages it
// covered are gone.
func (h *Hub) ackBound(ctx context.Context, msg protocol.Message) *routingError {
	if msg.ContactOp != protocol.ContactOpAck || msg.ContactAckUpto == 0 {
		return nil
	}
	known, err := h.store.ContactOperationKnown(ctx, msg.MessageID)
	if err != nil {
		return &routingError{protocol.ErrInternal, "contact messages unavailable"}
	}
	if known {
		return nil
	}
	newest, err := h.store.PeerMessages().NewestReceivedIn(ctx, msg.ContactKey, msg.ContactEpoch)
	switch {
	case errors.Is(err, messaging.ErrInvalid):
		return &routingError{protocol.ErrMalformedFrame, "contact_epoch names no pairing with this contact"}
	case err != nil:
		return &routingError{protocol.ErrInternal, "contact messages unavailable"}
	case msg.ContactAckUpto > newest:
		return &routingError{protocol.ErrMalformedFrame, store.ErrAckBound.Error()}
	}
	return nil
}

// contactCounts are what each contact's snapshot row counts: its review
// requests awaiting the operator, and its acknowledgments.
type contactCounts struct {
	awaiting map[string]int
	acks     map[string]protocol.ContactAckSummary
}

func (h *Hub) contactCounts(ctx context.Context) (contactCounts, error) {
	var c contactCounts
	var err error
	if c.awaiting, err = h.store.PeerMessages().AwaitingReviewCounts(ctx); err != nil {
		return c, err
	}
	c.acks, err = h.store.PeerMessages().AckSummaries(ctx)
	return c, err
}
