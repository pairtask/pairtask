package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

// Seed only bulk capacity rows; the tested boundary transitions always use
// the real Commit/Accept/DeliverExchange and UnpairContact APIs.
func seedContactCaps(t *testing.T, f *operatorContactFixture, active, retired int) {
	t.Helper()
	tx, err := f.raw.Begin()
	pairOK(t, err)
	defer tx.Rollback()
	for i := 0; i < active+retired; i++ {
		ended := ""
		if i >= active {
			ended = messageAt.Format(time.RFC3339Nano)
		}
		_, err := tx.Exec(`INSERT INTO peer_contacts(identity_key,name,destination,established_at,joined,invited,unpaired_at) VALUES (?,'bulk','',?,0,1,?)`, joinKey(10000+i), messageAt.Format(time.RFC3339Nano), ended)
		pairOK(t, err)
	}
	pairOK(t, tx.Commit())
}
func capTryInstall(t *testing.T, f *operatorContactFixture, n int) error {
	t.Helper()
	o := f.epochOutcome(t, n)
	if _, err := f.db.Commit(context.Background(), o); err != nil {
		return err
	}
	if _, err := f.db.Accept(context.Background(), f.contact); err != nil {
		return err
	}
	_, err := f.db.DeliverExchange(context.Background(), peer.ExchangeDelivery{IdentityKey: f.contact, OperationID: o.OperationID, Decision: peer.PairingAccepted, At: messageAt})
	return err
}
