package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func TestRemotePausedClearAcceptancePreservesGenerationWatermark(t *testing.T) {
	f, c, id := controlStoreFixture(t)
	controlAccept(t, c, messaging.ContactState{PairingID: id, Gen: 5, State: 1})
	full, err := f.db.PeerMessages().Admit(context.Background(), f.contact)
	pairOK(t, err)
	pairOK(t, full.ClearRemotePaused(context.Background()))
	state, gen := controlPeerState(t, f)
	pairEqual(t, state, "")
	pairEqual(t, gen, int64(5))
	controlAccept(t, c, messaging.ContactState{PairingID: id, Gen: 4, State: 1})
	state, gen = controlPeerState(t, f)
	pairEqual(t, state, "")
	pairEqual(t, gen, int64(5))
}
func TestRemotePausedClearAcceptanceCannotClearTerminalOrRefresh(t *testing.T) {
	f, c, id := controlStoreFixture(t)
	full, err := f.db.PeerMessages().Admit(context.Background(), f.contact)
	pairOK(t, err)
	controlAccept(t, c, messaging.ContactState{PairingID: id, Gen: 5, State: 3})
	err = full.ClearRemotePaused(context.Background())
	pairEqual(t, errors.Is(err, store.ErrFenced), true)
	state, gen := controlPeerState(t, f)
	pairEqual(t, state, "unpaired")
	pairEqual(t, gen, int64(5))
	err = f.db.PeerMessages().ClearRemotePaused(context.Background())
	pairEqual(t, errors.Is(err, store.ErrFenced), true)
}
