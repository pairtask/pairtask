package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/store"
)

func pairOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func pairEqual[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}
func pairOutcome(n int) peer.PairingOutcome {
	return peer.PairingOutcome{OperationID: fmt.Sprintf("%032x", n), InvitationID: fmt.Sprintf("%032x", n+100), IdentityKey: strings.Repeat("a", 64), Name: "Alice", TranscriptID: bytes.Repeat([]byte{7}, 64), At: time.Date(2026, 10, 27, 12, 0, n, 0, time.UTC)}
}
func pairSQLite(t *testing.T, path string) *store.Store {
	t.Helper()
	db, err := store.Open(path)
	pairOK(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}
func pairBackends(t *testing.T, check func(*testing.T, peer.PairingStore)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) { check(t, peer.NewMemPairingStore()) })
	t.Run("sqlite", func(t *testing.T) { check(t, pairSQLite(t, filepath.Join(t.TempDir(), "pairings.db"))) })
}
func pairSaved(t *testing.T, db peer.PairingStore, o peer.PairingOutcome) peer.PairingRequest {
	t.Helper()
	row, err := db.Commit(context.Background(), o)
	pairOK(t, err)
	return row
}
func pairReceipt(t *testing.T, db peer.PairingStore, op string) peer.PairingOutcome {
	t.Helper()
	value, ok, err := db.Outcome(context.Background(), op)
	pairOK(t, err)
	pairEqual(t, ok, true)
	return value
}

func TestPairingStoreAcceptanceReplayAndDecisions(t *testing.T) {
	pairBackends(t, func(t *testing.T, db peer.PairingStore) {
		ctx := context.Background()
		first := pairOutcome(1)
		pairEqual(t, pairSaved(t, db, first).State, peer.PairingPending)
		pairEqual(t, pairSaved(t, db, first).State, peer.PairingPending)
		accepted, err := db.Accept(ctx, first.IdentityKey)
		pairOK(t, err)
		pairEqual(t, accepted.State, peer.PairingAccepted)
		_, err = db.Accept(ctx, first.IdentityKey)
		pairOK(t, err)
		_, err = db.Reject(ctx, first.IdentityKey)
		pairEqual(t, errors.Is(err, peer.ErrNotPending), true)
		again := pairOutcome(2)
		pairEqual(t, pairSaved(t, db, again).State, peer.PairingAccepted)
		pairEqual(t, pairReceipt(t, db, first.OperationID).InvitationID, first.InvitationID)
		pairEqual(t, pairReceipt(t, db, again.OperationID).InvitationID, again.InvitationID)
		rows, err := db.Requests(ctx)
		pairOK(t, err)
		pairEqual(t, len(rows), 1)
	})
}

func TestPairingStoreAcceptanceRejectedIdentityStaysRejected(t *testing.T) {
	pairBackends(t, func(t *testing.T, db peer.PairingStore) {
		ctx := context.Background()
		first := pairOutcome(3)
		pairSaved(t, db, first)
		row, err := db.Reject(ctx, first.IdentityKey)
		pairOK(t, err)
		pairEqual(t, row.State, peer.PairingRejected)
		_, err = db.Reject(ctx, first.IdentityKey)
		pairOK(t, err)
		_, err = db.Accept(ctx, first.IdentityKey)
		pairEqual(t, errors.Is(err, peer.ErrNotPending), true)
		pairEqual(t, pairSaved(t, db, pairOutcome(4)).State, peer.PairingRejected)
		changed := pairOutcome(5)
		changed.IdentityKey = strings.Repeat("b", 64)
		// Same name is not identity and cannot inherit the old decision.
		pairEqual(t, pairSaved(t, db, changed).State, peer.PairingPending)
		rows, err := db.Requests(ctx)
		pairOK(t, err)
		pairEqual(t, len(rows), 2)
	})
}

func pairConflict(t *testing.T, db peer.PairingStore, original peer.PairingOutcome, mutate func(*peer.PairingOutcome)) {
	t.Helper()
	altered := original
	altered.TranscriptID = bytes.Clone(original.TranscriptID)
	mutate(&altered)
	_, err := db.Commit(context.Background(), altered)
	pairEqual(t, errors.Is(err, peer.ErrOutcomeConflict), true)
	receipt := pairReceipt(t, db, original.OperationID)
	pairEqual(t, receipt.IdentityKey, original.IdentityKey)
	pairEqual(t, bytes.Equal(receipt.TranscriptID, original.TranscriptID), true)
	rows, err := db.Requests(context.Background())
	pairOK(t, err)
	pairEqual(t, len(rows), 1)
}
func TestPairingStoreAcceptanceOutcomeContentCannotChange(t *testing.T) {
	mutations := map[string]func(*peer.PairingOutcome){
		"identity":   func(o *peer.PairingOutcome) { o.IdentityKey = strings.Repeat("b", 64) },
		"name":       func(o *peer.PairingOutcome) { o.Name = "Mallory" },
		"invitation": func(o *peer.PairingOutcome) { o.InvitationID = strings.Repeat("f", 32) },
		"transcript": func(o *peer.PairingOutcome) { o.TranscriptID[0] ^= 1 },
		"time":       func(o *peer.PairingOutcome) { o.At = o.At.Add(time.Second) },
	}
	pairBackends(t, func(t *testing.T, db peer.PairingStore) {
		original := pairOutcome(6)
		pairSaved(t, db, original)
		for name, mutate := range mutations {
			t.Run(name, func(t *testing.T) { pairConflict(t, db, original, mutate) })
		}
	})
}

func TestPairingStoreAcceptanceOneWinnerPerInvitation(t *testing.T) {
	pairBackends(t, func(t *testing.T, db peer.PairingStore) {
		var wg sync.WaitGroup
		results := make(chan error, 12)
		for n := 1; n <= 12; n++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				o := pairOutcome(n)
				o.InvitationID = strings.Repeat("f", 32)
				o.IdentityKey = fmt.Sprintf("%064x", n)
				_, err := db.Commit(context.Background(), o)
				results <- err
			}(n)
		}
		wg.Wait()
		close(results)
		successes := 0
		for err := range results {
			if err == nil {
				successes++
				continue
			}
			pairEqual(t, errors.Is(err, peer.ErrOutcomeConflict), true)
		}
		pairEqual(t, successes, 1)
		rows, err := db.Requests(context.Background())
		pairOK(t, err)
		pairEqual(t, len(rows), 1)
	})
}

func TestPairingStoreAcceptanceCopiesTranscript(t *testing.T) {
	pairBackends(t, func(t *testing.T, db peer.PairingStore) {
		original := pairOutcome(7)
		want := bytes.Clone(original.TranscriptID)
		pairSaved(t, db, original)
		clear(original.TranscriptID)
		receipt := pairReceipt(t, db, original.OperationID)
		pairEqual(t, bytes.Equal(receipt.TranscriptID, want), true)
		clear(receipt.TranscriptID)
		pairEqual(t, bytes.Equal(pairReceipt(t, db, original.OperationID).TranscriptID, want), true)
	})
}

func TestPairingStoreAcceptanceRestartRetainsAllOutcomes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairings.db")
	db := pairSQLite(t, path)
	first, second := pairOutcome(8), pairOutcome(9)
	pairSaved(t, db, first)
	_, err := db.Accept(context.Background(), first.IdentityKey)
	pairOK(t, err)
	pairSaved(t, db, second)
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	pairEqual(t, pairSaved(t, db, first).State, peer.PairingAccepted)
	pairEqual(t, pairSaved(t, db, second).State, peer.PairingAccepted)
	pairEqual(t, pairReceipt(t, db, first.OperationID).InvitationID, first.InvitationID)
	pairEqual(t, pairReceipt(t, db, second.OperationID).InvitationID, second.InvitationID)
	rows, err := db.Requests(context.Background())
	pairOK(t, err)
	pairEqual(t, len(rows), 1)
}

func TestPairingStoreAcceptanceCommitFailureRollsBackRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairings.db")
	db := pairSQLite(t, path)
	raw, err := sql.Open("sqlite", path)
	pairOK(t, err)
	defer raw.Close()
	_, err = raw.Exec(`CREATE TRIGGER fail_pairing_outcome BEFORE INSERT ON peer_pairing_outcomes BEGIN SELECT RAISE(ABORT,'injected outcome failure'); END`)
	pairOK(t, err)
	original := pairOutcome(10)
	_, err = db.Commit(context.Background(), original)
	if err == nil {
		t.Fatal("injected outcome failure returned success")
	}
	rows, err := db.Requests(context.Background())
	pairOK(t, err)
	pairEqual(t, len(rows), 0)
	_, ok, err := db.Outcome(context.Background(), original.OperationID)
	pairOK(t, err)
	pairEqual(t, ok, false)
	_, err = raw.Exec(`DROP TRIGGER fail_pairing_outcome`)
	pairOK(t, err)
	pairEqual(t, pairSaved(t, db, original).State, peer.PairingPending)
}
