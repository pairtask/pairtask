package hub

import (
	"context"
	"errors"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// contactPause pauses or resumes a contact (#276) with its audit row, and
// answers the pairing snapshot, so every view shows it at once. A pause
// moves the contact's generation on, so nothing a session admitted before
// it changes anything more; its live link, either side's, is then halted.
// A resume asks for a session and an import pass, so what waited moves.
func (h *Hub) contactPause(ctx context.Context, req operatorRequest, auditID int64) (protocol.Message, error) {
	msg := req.msg
	paused := msg.ContactOp == protocol.ContactOpPause
	_, applied, err := h.store.ApplyContactPause(ctx, store.ContactPause{OperationID: msg.MessageID, Contact: msg.ContactKey,
		Paused: paused, AuditID: auditID, At: h.pair.cfg.Clock()})
	if err != nil {
		return protocol.Message{}, err
	}
	if applied { // a retry changed nothing now, so acts on nothing (#276)
		h.afterPause(msg.ContactKey, paused)
	}
	snapshot, rerr := h.applyPairing(ctx, pairingOps[protocol.PairingOpList], "")
	if rerr != nil {
		return rerr.frame(msg.MessageID), nil
	}
	return protocol.Message{Type: protocol.TypeOperatorContactResult, MessageID: msg.MessageID, ContactKey: msg.ContactKey,
		ContactOp: msg.ContactOp, PairingResult: snapshot}, nil
}

// afterPause acts on a committed change: a pause halts the live link, a
// resume asks for a session and for what waited to be imported.
func (h *Hub) afterPause(contact string, paused bool) {
	if paused {
		h.dropLive(contact)
	} else {
		h.SyncMessages(contact)
		h.importsDue()
	}
	h.PairingChanged()
	h.MessagesChanged(contact)
}

// pauseRefusal is what the operator hears of a change refused because the
// contact is paused, or a session fenced by a pause.
func pauseRefusal(err error) (*routingError, bool) {
	switch {
	case errors.Is(err, store.ErrPaused):
		return &routingError{protocol.ErrContactPaused, "paused: /pair resume to send"}, true
	case errors.Is(err, store.ErrFenced):
		return &routingError{protocol.ErrContactPaused, "paused or resumed meanwhile; try again"}, true
	}
	return nil, false
}
