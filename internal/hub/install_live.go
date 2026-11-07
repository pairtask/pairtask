package hub

import (
	"context"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

// An install ends the contact's old live link at once (#276): the fence
// already keeps that link from changing anything, but an idle one would
// stay open on its blocked read until its next round. Only the call that
// installed acts; a replay leaves the fresh pairing's link alone.

// installedDelivery is the inviter's pairing store, closing the contact's
// live link after a delivery that installed.
type installedDelivery struct {
	peer.PairingStore
	installed func(contact string)
}

func (s installedDelivery) DeliverExchange(ctx context.Context, d peer.ExchangeDelivery) (peer.InstallResult, error) {
	out, err := s.PairingStore.DeliverExchange(ctx, d)
	if err == nil && out.Changed {
		s.installed(out.Exchange.IdentityKey)
	}
	return out, err
}

// exchangeLedger reads one exchange's ledger row.
type exchangeLedger interface {
	Exchange(ctx context.Context, operationID string) (peer.ExchangeRecord, bool, error)
}

// installedJoin is the joiner's store, closing the contact's live link
// after a confirmed decision that installed the join's exchange: it was
// not installed before the confirmation and is after.
type installedJoin struct {
	peer.JoinStore
	ledger    exchangeLedger
	installed func(contact string)
}

func (s installedJoin) RecordDecisionConfirmed(ctx context.Context, opID string, decision peer.PairingState, at time.Time) (peer.JoinRecord, error) {
	before := s.installedAt(ctx, opID)
	r, err := s.JoinStore.RecordDecisionConfirmed(ctx, opID, decision, at)
	if err == nil && s.newlyInstalled(ctx, opID, before) {
		s.installed(r.Peer)
	}
	return r, err
}

// newlyInstalled: the exchange was not installed before, and is now.
func (s installedJoin) newlyInstalled(ctx context.Context, opID string, before time.Time) bool {
	if !before.IsZero() {
		return false
	}
	return !s.installedAt(ctx, opID).IsZero()
}

// installedAt is when the exchange installed, zero while it hasn't or
// can't be read.
func (s installedJoin) installedAt(ctx context.Context, opID string) time.Time {
	x, found, err := s.ledger.Exchange(ctx, opID)
	if err != nil || !found {
		return time.Time{}
	}
	return x.InstalledAt
}

// pairingStore is the inviter's store; without one it stays as it was.
func (h *Hub) pairingStore() peer.PairingStore {
	if h.store == nil {
		return h.store
	}
	return installedDelivery{PairingStore: h.store, installed: h.dropLive}
}

// joinStore is the joiner's store; without one it stays as it was.
func (h *Hub) joinStore() peer.JoinStore {
	if h.store == nil {
		return h.store
	}
	return installedJoin{JoinStore: h.store, ledger: h.store, installed: h.dropLive}
}
