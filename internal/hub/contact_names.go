package hub

import (
	"context"
	"errors"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// contactName sets what this side calls a contact, finishing its audit row
// with the change, and answers the pairing snapshot, so every view of the
// contact shows the name at once.
func (h *Hub) contactName(ctx context.Context, req operatorRequest, auditID int64) (protocol.Message, error) {
	msg := req.msg
	if _, err := h.store.SetContactLocalName(ctx, store.ContactName{Contact: msg.ContactKey, Name: msg.ContactText, AuditID: auditID}); err != nil {
		return protocol.Message{}, err
	}
	h.PairingChanged()
	snapshot, rerr := h.applyPairing(ctx, pairingOps[protocol.PairingOpList], "")
	if rerr != nil {
		return rerr.frame(msg.MessageID), nil
	}
	return protocol.Message{Type: protocol.TypeOperatorContactResult, MessageID: msg.MessageID, ContactKey: msg.ContactKey,
		ContactOp: msg.ContactOp, PairingResult: snapshot}, nil
}

// nameRefusal is what the operator hears of a refused name.
func nameRefusal(err error) (*routingError, bool) {
	switch {
	case errors.Is(err, store.ErrNoContact):
		return &routingError{protocol.ErrContactRefused, "not a contact"}, true
	case errors.Is(err, peer.ErrInvalidPairingName):
		return &routingError{protocol.ErrContactRefused, "a name is at most 64 bytes of printable text"}, true
	}
	return nil, false
}
