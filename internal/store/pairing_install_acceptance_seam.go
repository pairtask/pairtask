package store

import (
	"context"
	"github.com/pairtask/pairtask/internal/peer"
)

// Legacy delegates only: no desired ledger or epoch install behavior.
func (s *Store) PairingMark(ctx context.Context) (uint64, error) {
	var mark uint64
	err := s.db.QueryRowContext(ctx, `SELECT mark FROM pairing_marks WHERE id=1`).Scan(&mark)
	return mark, err
}
func (s *Store) Exchange(ctx context.Context, op string) (peer.ExchangeRecord, bool, error) {
	return peer.ExchangeRecord{}, false, nil
}
func (s *Store) DeliverExchange(ctx context.Context, d peer.ExchangeDelivery) (peer.InstallResult, error) {
	_, err := s.Deliver(ctx, d.IdentityKey, d.Decision, d.At)
	return peer.InstallResult{}, err
}
