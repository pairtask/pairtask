package messaging

import (
	"context"
	"time"
)

// StoreError is a session that failed in this side's own store, as opposed
// to on the wire: a caller tells the two apart with errors.As.
type StoreError struct{ Err error }

func (e StoreError) Error() string { return "messaging store: " + e.Err.Error() }
func (e StoreError) Unwrap() error { return e.Err }

func storeErr(err error) error {
	if err == nil {
		return nil
	}
	return StoreError{err}
}

// sessionStore marks every error of the store calls a session makes.
type sessionStore struct{ Store }

func (s sessionStore) Offer(ctx context.Context, contact string, now time.Time, maxCount, maxBytes int) ([]Row, error) {
	rows, err := s.Store.Offer(ctx, contact, now, maxCount, maxBytes)
	return rows, storeErr(err)
}

func (s sessionStore) Delivered(ctx context.Context, contact, id string, at time.Time) error {
	return storeErr(s.Store.Delivered(ctx, contact, id, at))
}

func (s sessionStore) Refused(ctx context.Context, contact, id string, r Refusal, at time.Time) error {
	return storeErr(s.Store.Refused(ctx, contact, id, r, at))
}

func (s sessionStore) Accept(ctx context.Context, contact string, m Message, now time.Time) (Answer, error) {
	answer, err := s.Store.Accept(ctx, contact, m, now)
	return answer, storeErr(err)
}

func (s sessionStore) PendingReads(ctx context.Context, contact string, max int) ([]string, error) {
	ids, err := s.Store.PendingReads(ctx, contact, max)
	return ids, storeErr(err)
}

func (s sessionStore) ReadConfirmed(ctx context.Context, contact, id string) error {
	return storeErr(s.Store.ReadConfirmed(ctx, contact, id))
}

func (s sessionStore) PeerRead(ctx context.Context, contact, id string, at time.Time) error {
	return storeErr(s.Store.PeerRead(ctx, contact, id, at))
}
