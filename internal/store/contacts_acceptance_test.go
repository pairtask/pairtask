package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

type contactBackend struct {
	joins    peer.JoinStore
	pairs    peer.PairingStore
	contacts peer.ContactStore
}

func contactBackends(t *testing.T, check func(*testing.T, contactBackend)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) {
		shared := peer.NewMemContacts()
		joins := peer.NewMemJoinStoreWith(shared)
		pairs := peer.NewMemPairingStoreWith(shared)
		check(t, contactBackend{joins, pairs, joins})
	})
	t.Run("sqlite", func(t *testing.T) {
		db := pairSQLite(t, filepath.Join(t.TempDir(), "contacts.db"))
		check(t, contactBackend{db, db, db})
	})
}

func contactReady(t *testing.T, db peer.JoinStore, op string) peer.JoinRecord {
	t.Helper()
	ctx := context.Background()
	_, err := db.BeginJoin(ctx, peer.JoinStart{OperationID: op, Joiner: joinKey(1), Name: "Local name", Destination: "peer.test:7445", At: joinAt})
	pairOK(t, err)
	_, err = db.SetCorrelation(ctx, op, peer.InviterHello{InvitationID: "inv-" + op, OperationID: "remote-" + op}, joinAt)
	pairOK(t, err)
	r, err := db.RecordVerified(ctx, op, peer.ExchangeResult{Peer: joinKey(2), TranscriptID: []byte("verified-transcript")}, joinAt)
	pairOK(t, err)
	r, err = db.RecordReceipt(ctx, op, joinReceipt(r), joinAt)
	pairOK(t, err)
	return r
}

func contactsCount(t *testing.T, db peer.ContactStore, want int) {
	t.Helper()
	rows, err := db.Contacts(context.Background())
	pairOK(t, err)
	pairEqual(t, len(rows), want)
}

func contactGet(t *testing.T, db peer.ContactStore, key string) peer.Contact {
	t.Helper()
	row, found, err := db.Contact(context.Background(), key)
	pairOK(t, err)
	pairEqual(t, found, true)
	return row
}

func TestContactsAcceptanceDecisionAndConfirmationAreSeparate(t *testing.T) {
	contactBackends(t, func(t *testing.T, db contactBackend) {
		ctx := context.Background()
		r := contactReady(t, db.joins, "one")
		contactsCount(t, db.contacts, 0)
		at := joinAt.Add(time.Hour)
		r, err := db.joins.RecordDecision(ctx, r.OperationID, peer.PairingAccepted, at)
		pairOK(t, err)
		pairEqual(t, r.State, peer.JoinContact)
		pairEqual(t, r.DecisionConfirmedAt.IsZero(), true)
		contact := contactGet(t, db.contacts, r.Peer)
		pairEqual(t, contact.Joined, true)
		pairEqual(t, contact.Invited, false)
		pairEqual(t, contact.Destination, "peer.test:7445")
		pairEqual(t, contact.EstablishedAt.Equal(at), true)
		confirmed, err := db.joins.RecordDecisionConfirmed(ctx, r.OperationID, peer.PairingAccepted, at.Add(time.Minute))
		pairOK(t, err)
		pairEqual(t, confirmed.DecisionConfirmedAt.Equal(at.Add(time.Minute)), true)
		_, err = db.joins.RecordDecisionConfirmed(ctx, r.OperationID, peer.PairingAccepted, at.Add(time.Hour))
		pairOK(t, err)
		joinUnchanged(t, db.joins, confirmed)
		_, err = db.joins.RecordDecision(ctx, r.OperationID, peer.PairingRejected, at)
		pairEqual(t, errors.Is(err, peer.ErrJoinConflict), true)
		_, err = db.joins.RecordDecisionConfirmed(ctx, r.OperationID, peer.PairingRejected, at)
		pairEqual(t, errors.Is(err, peer.ErrJoinConflict), true)
		contactsCount(t, db.contacts, 1)
	})
}

func TestContactsAcceptanceCannotSkipReceiptOrDecision(t *testing.T) {
	contactBackends(t, func(t *testing.T, db contactBackend) {
		ctx := context.Background()
		r := joinVerified(t, db.joins, "unknown")
		_, err := db.joins.RecordDecision(ctx, r.OperationID, peer.PairingAccepted, joinAt)
		pairEqual(t, errors.Is(err, peer.ErrJoinState), true)
		joinUnchanged(t, db.joins, r)
		ready := contactReady(t, db.joins, "ready")
		_, err = db.joins.RecordDecisionConfirmed(ctx, ready.OperationID, peer.PairingAccepted, joinAt)
		pairEqual(t, errors.Is(err, peer.ErrJoinState), true)
		joinUnchanged(t, db.joins, ready)
		contactsCount(t, db.contacts, 0)
	})
}

func TestContactsAcceptanceRejectedCreatesNoContact(t *testing.T) {
	contactBackends(t, func(t *testing.T, db contactBackend) {
		ctx := context.Background()
		r := contactReady(t, db.joins, "declined")
		r, err := db.joins.RecordDecision(ctx, r.OperationID, peer.PairingRejected, joinAt)
		pairOK(t, err)
		pairEqual(t, r.State, peer.JoinDeclined)
		_, err = db.joins.RecordDecision(ctx, r.OperationID, peer.PairingRejected, joinAt.Add(time.Hour))
		pairOK(t, err)
		joinUnchanged(t, db.joins, r)
		outcome := pairOutcome(1)
		pairSaved(t, db.pairs, outcome)
		_, err = db.pairs.Reject(ctx, outcome.IdentityKey)
		pairOK(t, err)
		_, err = db.pairs.Deliver(ctx, outcome.IdentityKey, peer.PairingRejected, joinAt)
		pairOK(t, err)
		contactsCount(t, db.contacts, 0)
	})
}

func TestContactsAcceptanceInviterNeedsMatchingAcknowledgment(t *testing.T) {
	contactBackends(t, func(t *testing.T, db contactBackend) {
		ctx := context.Background()
		outcome := pairOutcome(1)
		pairSaved(t, db.pairs, outcome)
		_, err := db.pairs.Deliver(ctx, outcome.IdentityKey, peer.PairingAccepted, joinAt)
		if err == nil {
			t.Fatal("pending request marked delivered")
		}
		_, err = db.pairs.Accept(ctx, outcome.IdentityKey)
		pairOK(t, err)
		contactsCount(t, db.contacts, 0)
		_, err = db.pairs.Deliver(ctx, outcome.IdentityKey, peer.PairingRejected, joinAt)
		if err == nil {
			t.Fatal("opposite decision marked delivered")
		}
		at := joinAt.Add(time.Hour)
		r, err := db.pairs.Deliver(ctx, outcome.IdentityKey, peer.PairingAccepted, at)
		pairOK(t, err)
		pairEqual(t, r.DeliveredAt.Equal(at), true)
		again, err := db.pairs.Deliver(ctx, outcome.IdentityKey, peer.PairingAccepted, at.Add(time.Hour))
		pairOK(t, err)
		pairEqual(t, again.DeliveredAt, r.DeliveredAt)
		contact := contactGet(t, db.contacts, outcome.IdentityKey)
		pairEqual(t, contact.Invited, true)
		pairEqual(t, contact.Joined, false)
		pairEqual(t, contact.Name, outcome.Name)
		contactsCount(t, db.contacts, 1)
	})
}

func TestContactsAcceptanceReciprocalPairingMergesIdentity(t *testing.T) {
	contactBackends(t, func(t *testing.T, db contactBackend) {
		ctx := context.Background()
		r := contactReady(t, db.joins, "joined")
		_, err := db.joins.RecordDecision(ctx, r.OperationID, peer.PairingAccepted, joinAt)
		pairOK(t, err)
		outcome := pairOutcome(1)
		outcome.IdentityKey = r.Peer
		pairSaved(t, db.pairs, outcome)
		_, err = db.pairs.Accept(ctx, r.Peer)
		pairOK(t, err)
		_, err = db.pairs.Deliver(ctx, r.Peer, peer.PairingAccepted, joinAt.Add(time.Hour))
		pairOK(t, err)
		contactsCount(t, db.contacts, 1)
		contact := contactGet(t, db.contacts, r.Peer)
		pairEqual(t, contact.Joined, true)
		pairEqual(t, contact.Invited, true)
		pairEqual(t, contact.Destination, r.Destination)
		pairEqual(t, contact.EstablishedAt.Equal(joinAt), true)
	})
}

func TestContactsAcceptanceSQLiteFailureRollsBackDecisionAndDelivery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.db")
	db := pairSQLite(t, path)
	r := contactReady(t, db, "one")
	outcome := pairOutcome(1)
	pairSaved(t, db, outcome)
	_, err := db.Accept(context.Background(), outcome.IdentityKey)
	pairOK(t, err)
	raw, err := sql.Open("sqlite", path)
	pairOK(t, err)
	defer raw.Close()
	_, err = raw.Exec("CREATE TRIGGER fail_contact BEFORE INSERT ON peer_contacts BEGIN SELECT RAISE(ABORT, 'injected contact failure'); END")
	pairOK(t, err)
	_, err = db.RecordDecision(context.Background(), r.OperationID, peer.PairingAccepted, joinAt)
	if err == nil {
		t.Fatal("decision succeeded despite contact write failure")
	}
	joinUnchanged(t, db, r)
	_, err = db.Deliver(context.Background(), outcome.IdentityKey, peer.PairingAccepted, joinAt)
	if err == nil {
		t.Fatal("delivery succeeded despite contact write failure")
	}
	request, found, err := db.Request(context.Background(), outcome.IdentityKey)
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, request.DeliveredAt.IsZero(), true)
	contactsCount(t, db, 0)
	_, err = raw.Exec("DROP TRIGGER fail_contact")
	pairOK(t, err)
	_, err = db.RecordDecision(context.Background(), r.OperationID, peer.PairingAccepted, joinAt)
	pairOK(t, err)
}

func TestContactsAcceptanceSQLiteRestartKeepsUnconfirmedDecision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.db")
	db := pairSQLite(t, path)
	r := contactReady(t, db, "one")
	r, err := db.RecordDecision(context.Background(), r.OperationID, peer.PairingAccepted, joinAt)
	pairOK(t, err)
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	_, err = db.RecoverJoins(context.Background(), joinAt.Add(time.Hour))
	pairOK(t, err)
	joinUnchanged(t, db, r)
	contactsCount(t, db, 1)
	_, err = db.RecordDecisionConfirmed(context.Background(), r.OperationID, peer.PairingAccepted, joinAt.Add(time.Hour))
	pairOK(t, err)
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	pairEqual(t, joinGet(t, db, r.OperationID).DecisionConfirmedAt.IsZero(), false)
	contactsCount(t, db, 1)
}

func TestContactsAcceptanceCapacityFailurePreservesPendingJoin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.db")
	db := pairSQLite(t, path)
	r := contactReady(t, db, "one")
	raw, err := sql.Open("sqlite", path)
	pairOK(t, err)
	defer raw.Close()
	tx, err := raw.Begin()
	pairOK(t, err)
	for n := range peer.MaxPairingIdentities {
		_, err = tx.Exec("INSERT INTO peer_contacts(identity_key,name,destination,established_at,joined,invited) VALUES(?,?,?,?,?,?)", joinKey(n+10000), fmt.Sprint(n), "", joinAt.Format(time.RFC3339Nano), 0, 1)
		pairOK(t, err)
	}
	pairOK(t, tx.Commit())
	_, err = db.RecordDecision(context.Background(), r.OperationID, peer.PairingAccepted, joinAt)
	pairEqual(t, errors.Is(err, peer.ErrContactCapacity), true)
	joinUnchanged(t, db, r)
	contactsCount(t, db, peer.MaxPairingIdentities)
}

func TestContactsAcceptanceConcurrentReciprocalUpdatesKeepBothDirections(t *testing.T) {
	contactBackends(t, func(t *testing.T, db contactBackend) {
		ctx := context.Background()
		r := contactReady(t, db.joins, "reciprocal")
		outcome := pairOutcome(1)
		outcome.IdentityKey = r.Peer
		pairSaved(t, db.pairs, outcome)
		_, err := db.pairs.Accept(ctx, r.Peer)
		pairOK(t, err)
		start := make(chan struct{})
		results := make(chan error, 16)
		var workers sync.WaitGroup
		for n := range 16 {
			workers.Add(1)
			go func(n int) {
				defer workers.Done()
				<-start
				var err error
				if n%2 == 0 {
					_, err = db.joins.RecordDecision(ctx, r.OperationID, peer.PairingAccepted, joinAt)
				} else {
					_, err = db.pairs.Deliver(ctx, r.Peer, peer.PairingAccepted, joinAt)
				}
				results <- err
			}(n)
		}
		close(start)
		workers.Wait()
		close(results)
		for err := range results {
			pairOK(t, err)
		}
		contact := contactGet(t, db.contacts, r.Peer)
		pairEqual(t, contact.Joined, true)
		pairEqual(t, contact.Invited, true)
		pairEqual(t, contact.Destination, r.Destination)
		contactsCount(t, db.contacts, 1)
	})
}

func TestContactsAcceptanceTerminalDecisionsFreeOpenJoinCapacity(t *testing.T) {
	contactBackends(t, func(t *testing.T, db contactBackend) {
		ctx := context.Background()
		for n := range peer.MaxOpenJoins {
			r := contactReady(t, db.joins, fmt.Sprintf("settled-%d", n))
			_, err := db.joins.RecordDecision(ctx, r.OperationID, peer.PairingAccepted, joinAt)
			pairOK(t, err)
		}
		_, err := db.joins.BeginJoin(ctx, peer.JoinStart{OperationID: "next", Joiner: joinKey(1), Name: "Bob", At: joinAt})
		pairOK(t, err)
		contactsCount(t, db.contacts, 1)
	})
}

func TestContactsAcceptanceMigrationDoesNotInventDeliveryOrContact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", path)
	pairOK(t, err)
	_, err = raw.Exec(`CREATE TABLE peer_pairing_requests (
identity_key TEXT PRIMARY KEY, name TEXT NOT NULL, state TEXT NOT NULL, first_paired_at TEXT NOT NULL)`)
	pairOK(t, err)
	_, err = raw.Exec(`INSERT INTO peer_pairing_requests VALUES(?,?,?,?)`, joinKey(2), "Known peer", "accepted", joinAt.Format(time.RFC3339Nano))
	pairOK(t, err)
	pairOK(t, raw.Close())
	db := pairSQLite(t, path)
	request, found, err := db.Request(context.Background(), joinKey(2))
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, request.State, peer.PairingAccepted)
	pairEqual(t, request.DeliveredAt.IsZero(), true)
	contactsCount(t, db, 0)
	_, err = db.Deliver(context.Background(), joinKey(2), peer.PairingAccepted, joinAt.Add(time.Hour))
	pairOK(t, err)
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	request, found, err = db.Request(context.Background(), joinKey(2))
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, request.DeliveredAt.IsZero(), false)
	contactsCount(t, db, 1)
}
