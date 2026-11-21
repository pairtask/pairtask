package messaging_test

import (
	"context"
	"errors"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
	"testing"
)

// Enable the experimental capability on this session's store only.
type identityEnabled struct{ *store.PeerMessages }

func (identityEnabled) ExchangesIdentity() bool { return true }

func TestIdentityExchangeDefaultsToVersion12(t *testing.T) {
	db, _ := sessionFixture(t)
	bound, err := db.PeerMessages().Admit(context.Background(), sessionContact)
	sessionOK(t, err)
	frames := &scriptFrames{input: [][]byte{epochSessionHello(t, 13), epochSessionID(t, bound.PairingID())}}
	_, version, _ := sessionSide(frames, bound).OpenServe(context.Background())
	if version != 12 {
		t.Fatalf("default broker negotiated %d with a v13 peer", version)
	}
}

func TestIdentityPhaseRefusesEarlyMessage(t *testing.T) {
	ctx := context.Background()
	db, _ := sessionFixture(t)
	bound, err := db.PeerMessages().Admit(ctx, sessionContact)
	if err != nil {
		t.Fatal(err)
	}
	msg := messaging.Message{ID: "early", N: 1, CreatedAt: sessionAt, Text: "before identity end"}
	frames := &scriptFrames{input: [][]byte{epochSessionHello(t, 13), epochSessionID(t, bound.PairingID()), sessionMessage(t, msg)}}
	_, version, err := sessionSide(frames, identityEnabled{bound}).OpenServe(ctx)
	if version != 13 || !errors.Is(err, messaging.ErrProtocol) {
		t.Fatalf("negotiated %d, error %v", version, err)
	}
	rows, _, err := db.PeerMessages().Page(ctx, sessionContact, 0, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("after protocol error: %d messages, error %v", len(rows), err)
	}
}
