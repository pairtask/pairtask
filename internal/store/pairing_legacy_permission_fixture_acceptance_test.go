package store_test

import (
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func legacyRefusalFixture(t *testing.T, kind string) *operatorContactFixture {
	t.Helper()
	if kind == "non-null" {
		f := installFixture(t)
		f.installedEpoch(t, 101)
		return f
	}
	f := newOperatorContactFixture(t)
	switch kind {
	case "unpaired":
		_, _, err := f.db.UnpairContact(context.Background(), unpairRequest(t, f, "legacy-refusal-unpair"))
		pairOK(t, err)
	case "remote-unpaired":
		_, err := f.raw.Exec(`UPDATE peer_contacts SET peer_state='unpaired' WHERE identity_key=?`, f.contact)
		pairOK(t, err)
	case "retired-ledger":
		f.retireLegacyRepair(t)
	}
	return f
}

func (f *operatorContactFixture) retireLegacyRepair(t *testing.T) {
	t.Helper()
	o := f.epochOutcome(t, 101)
	o.Repair = f.contact
	_, err := f.db.Commit(context.Background(), o)
	pairOK(t, err)
	_, err = f.db.Reject(context.Background(), f.contact)
	pairOK(t, err)
	_, err = f.db.DeliverExchange(context.Background(), peer.ExchangeDelivery{IdentityKey: f.contact, OperationID: o.OperationID, Decision: peer.PairingRejected, At: messageAt})
	pairOK(t, err)
}
