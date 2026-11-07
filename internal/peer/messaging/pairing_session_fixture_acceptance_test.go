package messaging_test

import (
	"context"
	"errors"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

// A real consent-capable store with only the negotiated epoch surface observed.
type epochSessionStore struct {
	*store.PeerMessages
	id     []byte
	seen   []byte
	sawErr error
}

func (s *epochSessionStore) PairingID() []byte { return append([]byte(nil), s.id...) }
func (s *epochSessionStore) MinVersion() byte {
	if len(s.id) != 0 {
		return 9
	}
	return 1
}
func (s *epochSessionStore) SawVersion(_ context.Context, v byte) error {
	s.seen = append(s.seen, v)
	return s.sawErr
}
func epochSessionFixture(t *testing.T, id []byte) *epochSessionStore {
	t.Helper()
	db, _ := sessionFixture(t)
	bound, err := db.PeerMessages().Admit(context.Background(), sessionContact)
	sessionOK(t, err)
	return &epochSessionStore{PeerMessages: bound, id: id}
}
func epochSessionHello(t *testing.T, version byte) []byte {
	t.Helper()
	b, err := messaging.EncodeHello(messaging.Hello{Min: 1, Max: version})
	return sessionFrame(t, b, err)
}
func epochSessionID(t *testing.T, id []byte) []byte {
	t.Helper()
	b, err := messaging.EncodePairingEpoch(messaging.PairingEpoch{PairingID: id})
	return sessionFrame(t, b, err)
}
func epochSessionOpen(s messaging.Session, dial bool) (messaging.Result, byte, error) {
	if dial {
		return s.OpenDial(context.Background())
	}
	return s.OpenServe(context.Background())
}
func epochSessionKinds(t *testing.T, frames *scriptFrames) []byte {
	t.Helper()
	var kinds []byte
	for _, b := range frames.sent {
		k, err := messaging.Kind(b)
		sessionOK(t, err)
		kinds = append(kinds, k)
	}
	return kinds
}
func epochSessionError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("got %v want %v", err, want)
	}
}
