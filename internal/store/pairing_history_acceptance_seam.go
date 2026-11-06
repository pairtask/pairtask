package store

import "context"

// Compile-only DTO/header seams and legacy delegates; no desired history behavior.
type MessageKey struct {
	Contact string
	Epoch   int64
	ID      string
}

func (p *PeerMessages) AskOutIn(ctx context.Context, k MessageKey) (AskOut, bool, error) {
	return p.AskOutFor(ctx, k.Contact, k.ID)
}
func (p *PeerMessages) HeldStatusIn(ctx context.Context, k MessageKey) (HeldStatus, bool, error) {
	return p.HeldStatus(ctx, k.Contact, k.ID)
}
func (p *PeerMessages) AskStatusIn(ctx context.Context, k MessageKey) (AskStatus, bool, error) {
	return p.AskStatus(ctx, k.Contact, k.ID)
}
func (s *Store) HomeRoutingIn(ctx context.Context, k MessageKey) (HomeRoute, error) {
	return s.HomeRouting(ctx, k.Contact, k.ID)
}
func (s *Store) ContactEpoch(ctx context.Context, contact string) (int64, error) {
	return contactEpoch(ctx, s.db, contact)
}
