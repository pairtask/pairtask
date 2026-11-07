package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func installFixture(t *testing.T) *operatorContactFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "epoch-install.db")
	db := pairSQLite(t, path)
	raw, err := sql.Open("sqlite", path)
	pairOK(t, err)
	t.Cleanup(func() { raw.Close() })
	return &operatorContactFixture{db: db, raw: raw, contact: joinKey(11), path: path}
}

func (f *operatorContactFixture) epochOutcome(t *testing.T, n int) peer.PairingOutcome {
	t.Helper()
	mark, err := f.db.PairingMark(context.Background())
	pairOK(t, err)
	o := pairOutcome(n)
	o.IdentityKey, o.Mode, o.BegunMark = f.contact, peer.ModeEpoch, mark
	o.TranscriptID = bytes.Repeat([]byte{byte(n)}, 64)
	return o
}

func (f *operatorContactFixture) deliverEpoch(t *testing.T, o peer.PairingOutcome) peer.InstallResult {
	t.Helper()
	_, err := f.db.Accept(context.Background(), o.IdentityKey)
	pairOK(t, err)
	result, err := f.db.DeliverExchange(context.Background(), peer.ExchangeDelivery{IdentityKey: o.IdentityKey, OperationID: o.OperationID, Decision: peer.PairingAccepted, At: messageAt})
	pairOK(t, err)
	return result
}

func (f *operatorContactFixture) installedEpoch(t *testing.T, n int) peer.PairingOutcome {
	t.Helper()
	o := f.epochOutcome(t, n)
	_, err := f.db.Commit(context.Background(), o)
	pairOK(t, err)
	f.deliverEpoch(t, o)
	return o
}

func (f *operatorContactFixture) exchange(t *testing.T, id string) peer.ExchangeRecord {
	t.Helper()
	x, found, err := f.db.Exchange(context.Background(), id)
	pairOK(t, err)
	if !found {
		t.Fatalf("exchange ledger lacks operation %s", id)
	}
	return x
}

func (f *operatorContactFixture) installAuthority(t *testing.T) map[string][]string {
	t.Helper()
	result := map[string][]string{}
	for _, table := range []string{"peer_contacts", "peer_pairing_requests", "peer_pairing_outcomes", "peer_joins", "pairing_marks"} {
		result[table] = epochRows(t, f.raw, table, epochColumns(t, f.raw, table))
	}
	return result
}
