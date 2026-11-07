package hub

import (
	"context"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// tombstoneOps are what an unpaired contact still admits (#276): its
// history listed and read here, and the unpair itself, whose retry
// answers its first result. Anything else is contact_unpaired.
var tombstoneOps = map[string]bool{protocol.ContactOpList: true, protocol.ContactOpRead: true, protocol.ContactOpUnpair: true}

// errContactUnpaired refuses a change to a contact this side unpaired.
var errContactUnpaired = &routingError{protocol.ErrContactUnpaired, "unpaired: history kept; pair again to resume"}

// unpairedContact admits a tombstone operation on a contact this side
// unpaired and refuses any other; ok is false for a contact that isn't
// unpaired, which the caller checks as before.
func (h *Hub) unpairedContact(ctx context.Context, msg protocol.Message) (*routingError, bool) {
	unpaired, err := h.store.PeerMessages().Unpaired(ctx, msg.ContactKey)
	if err != nil || !unpaired {
		return nil, false
	}
	if tombstoneOps[msg.ContactOp] {
		return nil, true
	}
	return errContactUnpaired, true
}

// contactUnpair ends the contact's pairing (#276) with its audit row, and
// answers the durable result with the pairing snapshot. Only the call
// that changed it acts: the live link, either side's, is halted, the
// notice leaves at once and every view hears of it. A retry, even after
// the contact paired again, answers the first result and acts on nothing.
func (h *Hub) contactUnpair(ctx context.Context, req operatorRequest, auditID int64) (protocol.Message, error) {
	msg := req.msg
	out, applied, err := h.store.UnpairContact(ctx, store.ContactUnpair{OperationID: msg.MessageID, Contact: msg.ContactKey,
		Confirm: msg.ContactConfirm, SeenEpoch: msg.ContactEpoch, AuditID: auditID, At: h.pair.cfg.Clock()})
	if err != nil {
		return protocol.Message{}, err
	}
	if applied {
		h.afterUnpair(msg.ContactKey)
	}
	snapshot, rerr := h.applyPairing(ctx, pairingOps[protocol.PairingOpList], pairingArgs{})
	if rerr != nil {
		return rerr.frame(msg.MessageID), nil
	}
	view := protocol.ContactUnpairView{Changed: out.Changed, Generation: out.Generation, Epoch: out.Epoch, UnpairedAt: out.UnpairedAt}
	return protocol.Message{Type: protocol.TypeOperatorContactResult, MessageID: msg.MessageID, ContactKey: msg.ContactKey,
		ContactOp: msg.ContactOp, ContactUnpaired: &view, PairingResult: snapshot}, nil
}

// afterUnpair acts on a committed unpair: the live link halts, the notice
// is due at once (still under the per-contact bound) and the views hear.
func (h *Hub) afterUnpair(contact string) {
	h.dropLive(contact)
	h.noticesDue()
	h.PairingChanged()
	h.MessagesChanged(contact)
}
