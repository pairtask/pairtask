package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

func destinationReady(t *testing.T, s peer.JoinStore, op, address string, at time.Time) peer.JoinRecord {
	t.Helper()
	ctx := context.Background()
	_, err := s.BeginJoin(ctx, peer.JoinStart{OperationID: op, Joiner: joinKey(1), Name: "Local name", Destination: address, At: at})
	pairOK(t, err)
	_, err = s.SetCorrelation(ctx, op, peer.InviterHello{InvitationID: "inv-" + op, OperationID: "remote-" + op}, at)
	pairOK(t, err)
	r, err := s.RecordVerified(ctx, op, peer.ExchangeResult{Peer: joinKey(2), TranscriptID: []byte("verified-" + op)}, at)
	pairOK(t, err)
	r, err = s.RecordReceipt(ctx, op, joinReceipt(r), at)
	pairOK(t, err)
	return r
}

func destinationAccept(t *testing.T, s peer.JoinStore, r peer.JoinRecord, at time.Time) {
	t.Helper()
	_, err := s.RecordDecision(context.Background(), r.OperationID, peer.PairingAccepted, at)
	pairOK(t, err)
}

func TestContactDestinationAcceptanceNewPairingReplacesAddress(t *testing.T) {
	contactBackends(t, func(t *testing.T, db contactBackend) {
		first := destinationReady(t, db.joins, "first", "192.168.1.164:7445", joinAt)
		destinationAccept(t, db.joins, first, joinAt.Add(time.Minute))
		before := contactGet(t, db.contacts, first.Peer)
		newer := destinationReady(t, db.joins, "newer", "192.168.1.32:7445", joinAt.Add(time.Hour))
		// A verified exchange and receipt alone cannot replace the address.
		pairEqual(t, contactGet(t, db.contacts, first.Peer).Destination, before.Destination)
		destinationAccept(t, db.joins, newer, joinAt.Add(2*time.Hour))
		after := contactGet(t, db.contacts, first.Peer)
		pairEqual(t, after.Destination, newer.Destination)
		pairEqual(t, after.IdentityKey, before.IdentityKey)
		pairEqual(t, after.Name, before.Name)
		pairEqual(t, after.EstablishedAt.Equal(before.EstablishedAt), true)
		contactsCount(t, db.contacts, 1)
		// Replayed decisions and late confirmation of the first operation do
		// not restore its old endpoint.
		destinationAccept(t, db.joins, first, joinAt.Add(3*time.Hour))
		_, err := db.joins.RecordDecisionConfirmed(context.Background(), first.OperationID, peer.PairingAccepted, joinAt.Add(4*time.Hour))
		pairOK(t, err)
		pairEqual(t, contactGet(t, db.contacts, first.Peer).Destination, newer.Destination)
	})
}

func TestContactDestinationAcceptanceStartOrderSurvivesLateDecisions(t *testing.T) {
	contactBackends(t, func(t *testing.T, db contactBackend) {
		first := destinationReady(t, db.joins, "first", "first.test:7445", joinAt)
		destinationAccept(t, db.joins, first, joinAt)
		older := destinationReady(t, db.joins, "older", "older.test:7445", joinAt.Add(time.Hour))
		newer := destinationReady(t, db.joins, "newer", "first.test:7445", joinAt.Add(2*time.Hour))
		// Even a newer pairing to the SAME address advances its ordering.
		destinationAccept(t, db.joins, newer, joinAt.Add(3*time.Hour))
		destinationAccept(t, db.joins, older, joinAt.Add(4*time.Hour))
		pairEqual(t, contactGet(t, db.contacts, first.Peer).Destination, newer.Destination)
		tie := destinationReady(t, db.joins, "tie", "tie.test:7445", newer.CreatedAt)
		destinationAccept(t, db.joins, tie, joinAt.Add(5*time.Hour))
		pairEqual(t, contactGet(t, db.contacts, first.Peer).Destination, newer.Destination)
		empty := destinationReady(t, db.joins, "empty", "", joinAt.Add(6*time.Hour))
		destinationAccept(t, db.joins, empty, joinAt.Add(7*time.Hour))
		pairEqual(t, contactGet(t, db.contacts, first.Peer).Destination, newer.Destination)
	})
}

func TestContactDestinationAcceptanceRejectedPairingKeepsAddress(t *testing.T) {
	contactBackends(t, func(t *testing.T, db contactBackend) {
		first := destinationReady(t, db.joins, "first", "first.test:7445", joinAt)
		destinationAccept(t, db.joins, first, joinAt)
		declined := destinationReady(t, db.joins, "declined", "declined.test:7445", joinAt.Add(time.Hour))
		_, err := db.joins.RecordDecision(context.Background(), declined.OperationID, peer.PairingRejected, joinAt.Add(2*time.Hour))
		pairOK(t, err)
		pairEqual(t, contactGet(t, db.contacts, first.Peer).Destination, first.Destination)
	})
}

func TestContactDestinationAcceptanceLegacyMigrationAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	old, err := sql.Open("sqlite", path)
	pairOK(t, err)
	_, err = old.Exec(`CREATE TABLE peer_contacts (
identity_key TEXT PRIMARY KEY, name TEXT NOT NULL, destination TEXT NOT NULL,
established_at TEXT NOT NULL, joined INTEGER NOT NULL, invited INTEGER NOT NULL)`)
	pairOK(t, err)
	_, err = old.Exec(`INSERT INTO peer_contacts VALUES(?,?,?,?,?,?)`, joinKey(2), "My peer", "old.test:7445", joinAt.Format(time.RFC3339Nano), 1, 0)
	pairOK(t, err)
	pairOK(t, old.Close())
	db := pairSQLite(t, path)
	pairEqual(t, contactGet(t, db, joinKey(2)).Destination, "old.test:7445")
	newer := destinationReady(t, db, "newer", "new.test:7445", joinAt.Add(2*time.Hour))
	destinationAccept(t, db, newer, joinAt.Add(3*time.Hour))
	pairEqual(t, contactGet(t, db, joinKey(2)).Destination, newer.Destination)
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	older := destinationReady(t, db, "older", "stale.test:7445", joinAt.Add(time.Hour))
	destinationAccept(t, db, older, joinAt.Add(4*time.Hour))
	got := contactGet(t, db, joinKey(2))
	pairEqual(t, got.Destination, newer.Destination)
	pairEqual(t, got.Name, "My peer")
}

func TestContactDestinationAcceptanceAuditFailureRollsBackMove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	db := pairSQLite(t, path)
	first := destinationReady(t, db, "first", "old.test:7445", joinAt)
	destinationAccept(t, db, first, joinAt)
	newer := destinationReady(t, db, "newer", "new.test:7445", joinAt.Add(time.Hour))
	raw, err := sql.Open("sqlite", path)
	pairOK(t, err)
	defer raw.Close()
	_, err = raw.Exec(`CREATE TRIGGER deny_move_audit BEFORE INSERT ON operator_audit
WHEN NEW.action = 'contact destination' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`)
	pairOK(t, err)
	_, err = db.RecordDecision(context.Background(), newer.OperationID, peer.PairingAccepted, joinAt.Add(2*time.Hour))
	if err == nil {
		t.Fatal("address moved despite audit failure")
	}
	pairEqual(t, contactGet(t, db, first.Peer).Destination, first.Destination)
	pairEqual(t, joinGet(t, db, newer.OperationID).State, newer.State)
	_, err = raw.Exec(`DROP TRIGGER deny_move_audit`)
	pairOK(t, err)
	destinationAccept(t, db, newer, joinAt.Add(2*time.Hour))
	destinationAccept(t, db, newer, joinAt.Add(3*time.Hour))
	var count int
	pairOK(t, raw.QueryRow(`SELECT count(*) FROM operator_audit WHERE action='contact destination'`).Scan(&count))
	pairEqual(t, count, 1)
	var scope, detail, op string
	pairOK(t, raw.QueryRow(`SELECT scope, detail, COALESCE(message_id, '') FROM operator_audit WHERE action='contact destination'`).Scan(&scope, &detail, &op))
	pairEqual(t, scope, first.Peer)
	pairEqual(t, detail, "old.test:7445 -> new.test:7445")
	pairEqual(t, op, newer.OperationID)
}
