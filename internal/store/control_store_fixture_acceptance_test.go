package store_test

import (
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func controlStoreFixture(t *testing.T) (*operatorContactFixture, messaging.ControlStore, []byte) {
	t.Helper()
	f := installFixture(t)
	o := f.installedEpoch(t, 101)
	bound, err := f.db.PeerMessages().AdmitControl(context.Background(), f.contact)
	pairOK(t, err)
	id := f.exchange(t, o.OperationID)
	return f, bound, peer.PairingID(id.TranscriptID)
}
func controlPeerState(t *testing.T, f *operatorContactFixture) (string, int64) {
	t.Helper()
	var state string
	var gen int64
	err := f.raw.QueryRow(`SELECT peer_state,peer_state_gen FROM peer_contacts WHERE identity_key=?`, f.contact).Scan(&state, &gen)
	pairOK(t, err)
	return state, gen
}
func controlAccept(t *testing.T, c messaging.ControlStore, n messaging.ContactState) messaging.ControlAnswer {
	t.Helper()
	a, err := c.AcceptNotice(context.Background(), n)
	pairOK(t, err)
	return a
}
