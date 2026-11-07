package hub

import (
	"context"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Request and join rows say what each exchange is (#276): a first pairing,
// a pairing again with an identity this side unpaired, a declared repair
// or an address move. It is display only; the ledger decides.

// unpairedSince is when this side unpaired each listed contact.
func unpairedSince(contacts []protocol.PairingContactView) map[string]*time.Time {
	out := map[string]*time.Time{}
	for _, c := range contacts {
		if c.UnpairedAt != nil {
			out[c.IdentityKey] = c.UnpairedAt
		}
	}
	return out
}

// requestView is one request as listed, with its kind.
func requestView(r peer.PairingRequest, unpaired map[string]*time.Time) protocol.PairingRequestView {
	return protocol.PairingRequestView{IdentityKey: r.IdentityKey, Name: r.Name, State: string(r.State),
		FirstPairedAt: r.FirstPairedAt, DeliveredAt: timeOrNil(r.DeliveredAt),
		Kind: string(r.Kind), RepairsUnpairedAt: unpaired[r.IdentityKey]}
}

// joinKind adds the kind of the join's ledger exchange, once it has one.
func (h *Hub) joinKind(ctx context.Context, view *protocol.PairingJoinView, unpaired map[string]*time.Time) error {
	x, found, err := h.store.Exchange(ctx, view.OperationID)
	if err != nil || !found {
		return err
	}
	view.Kind = string(x.Kind)
	view.RepairsUnpairedAt = unpaired[x.IdentityKey]
	return nil
}
