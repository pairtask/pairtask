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
)

func pairRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	pairOK(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

func boundedOutcome(n int) peer.PairingOutcome {
	o := pairOutcome(n)
	o.IdentityKey = fmt.Sprintf("sha256:%064x", n)
	return o
}
func pairMissingReceipt(t *testing.T, db peer.PairingStore, op string) {
	t.Helper()
	_, found, err := db.Outcome(context.Background(), op)
	pairOK(t, err)
	pairEqual(t, found, false)
}
func pairCapacityRefusal(t *testing.T, db peer.PairingStore, o peer.PairingOutcome, want error) {
	t.Helper()
	_, err := db.Commit(context.Background(), o)
	if !errors.Is(err, want) {
		t.Fatalf("commit error = %v, want %v", err, want)
	}
	pairMissingReceipt(t, db, o.OperationID)
}

// Seed only the bulk setup. Tests exercise the final slot and all refusals,
// replays and decisions through the real store, without thousands of fsyncs.
func pairSeededBackends(t *testing.T, count int, outcome func(int) peer.PairingOutcome, state peer.PairingState, check func(*testing.T, peer.PairingStore)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) {
		db := peer.NewMemPairingStore()
		for n := 1; n <= count; n++ {
			o := outcome(n)
			pairSaved(t, db, o)
			if state == peer.PairingRejected {
				_, err := db.Reject(context.Background(), o.IdentityKey)
				pairOK(t, err)
			}
		}
		check(t, db)
	})
	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "pairings.db")
		db := pairSQLite(t, path)
		pairSeedSQLite(t, pairRaw(t, path), count, outcome, state)
		check(t, db)
	})
}

func pairSeedSQLite(t *testing.T, db *sql.DB, count int, outcome func(int) peer.PairingOutcome, state peer.PairingState) {
	t.Helper()
	tx, err := db.Begin()
	pairOK(t, err)
	defer tx.Rollback()
	for n := 1; n <= count; n++ {
		o := outcome(n)
		_, err := tx.Exec(`INSERT INTO peer_pairing_requests(identity_key,name,state,first_paired_at) VALUES(?,?,?,?) ON CONFLICT(identity_key) DO NOTHING`, o.IdentityKey, o.Name, string(state), o.At.Format(time.RFC3339Nano))
		pairOK(t, err)
		_, err = tx.Exec(`INSERT INTO peer_pairing_outcomes(operation_id,invitation_id,identity_key,name,transcript_id,at) VALUES(?,?,?,?,?,?)`, o.OperationID, o.InvitationID, o.IdentityKey, o.Name, o.TranscriptID, o.At.Format(time.RFC3339Nano))
		pairOK(t, err)
	}
	pairOK(t, tx.Commit())
}

func TestPairingBoundsAcceptanceValidatesNewFieldsAtomically(t *testing.T) {
	cases := map[string]func(*peer.PairingOutcome){
		"unprefixed key":      func(o *peer.PairingOutcome) { o.IdentityKey = strings.Repeat("a", 64) },
		"uppercase key":       func(o *peer.PairingOutcome) { o.IdentityKey = "sha256:" + strings.Repeat("A", 64) },
		"short key":           func(o *peer.PairingOutcome) { o.IdentityKey = "sha256:" + strings.Repeat("a", 63) },
		"nonhex key":          func(o *peer.PairingOutcome) { o.IdentityKey = "sha256:" + strings.Repeat("z", 64) },
		"long name":           func(o *peer.PairingOutcome) { o.Name = strings.Repeat("a", 65) },
		"multibyte long name": func(o *peer.PairingOutcome) { o.Name = strings.Repeat("é", 33) },
		"invalid UTF8":        func(o *peer.PairingOutcome) { o.Name = string([]byte{0xff}) },
		"control name":        func(o *peer.PairingOutcome) { o.Name = "Alice\nBob" },
		"escape name":         func(o *peer.PairingOutcome) { o.Name = "\x1b[2J" },
		"nonprintable name":   func(o *peer.PairingOutcome) { o.Name = "Alice\u202eBob" },
		"long operation":      func(o *peer.PairingOutcome) { o.OperationID = strings.Repeat("a", 129) },
		"long invitation":     func(o *peer.PairingOutcome) { o.InvitationID = strings.Repeat("a", 129) },
		"empty transcript":    func(o *peer.PairingOutcome) { o.TranscriptID = nil },
		"long transcript":     func(o *peer.PairingOutcome) { o.TranscriptID = bytes.Repeat([]byte{1}, 65) },
	}
	pairBackends(t, func(t *testing.T, db peer.PairingStore) {
		for name, mutate := range cases {
			t.Run(name, func(t *testing.T) {
				o := boundedOutcome(1)
				mutate(&o)
				_, err := db.Commit(context.Background(), o)
				if err == nil {
					t.Fatal("invalid outcome admitted")
				}
				pairMissingReceipt(t, db, o.OperationID)
				rows, err := db.Requests(context.Background())
				pairOK(t, err)
				pairEqual(t, len(rows), 0)
			})
		}
		for n, name := range []string{"", strings.Repeat("a", 64), strings.Repeat("é", 32)} {
			o := boundedOutcome(n + 100)
			o.Name = name
			o.OperationID = fmt.Sprintf("%0128d", n)
			o.InvitationID = fmt.Sprintf("%0128d", n+100)
			pairEqual(t, pairSaved(t, db, o).Name, name)
		}
	})
}

func TestPairingBoundsAcceptancePendingLimitAllowsKnownIdentityAndReplay(t *testing.T) {
	pairEqual(t, peer.MaxPendingPairingRequests, 64)
	pairBackends(t, func(t *testing.T, db peer.PairingStore) {
		for n := 1; n <= 64; n++ {
			pairSaved(t, db, boundedOutcome(n))
		}
		blocked := boundedOutcome(65)
		pairCapacityRefusal(t, db, blocked, peer.ErrTooManyPending)
		first := boundedOutcome(1)
		pairEqual(t, pairSaved(t, db, first).State, peer.PairingPending)
		known := boundedOutcome(66)
		known.IdentityKey = first.IdentityKey
		pairEqual(t, pairSaved(t, db, known).State, peer.PairingPending)
		_, err := db.Accept(context.Background(), first.IdentityKey)
		pairOK(t, err)
		pairEqual(t, pairSaved(t, db, blocked).State, peer.PairingPending)
		rows, err := db.Requests(context.Background())
		pairOK(t, err)
		pairEqual(t, len(rows), 65)
	})
}

func TestPairingBoundsAcceptanceConcurrentAdmissionRespectsLastSlot(t *testing.T) {
	pairBackends(t, func(t *testing.T, db peer.PairingStore) {
		for n := 1; n <= 63; n++ {
			pairSaved(t, db, boundedOutcome(n))
		}
		var wg sync.WaitGroup
		results := make(chan error, 12)
		for n := 64; n < 76; n++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				_, err := db.Commit(context.Background(), boundedOutcome(n))
				results <- err
			}(n)
		}
		wg.Wait()
		close(results)
		winners := 0
		for err := range results {
			if err == nil {
				winners++
			} else {
				pairEqual(t, errors.Is(err, peer.ErrTooManyPending), true)
			}
		}
		pairEqual(t, winners, 1)
		rows, err := db.Requests(context.Background())
		pairOK(t, err)
		pairEqual(t, len(rows), 64)
	})
}

func TestPairingBoundsAcceptanceIdentityLimitRetainsDecisions(t *testing.T) {
	pairEqual(t, peer.MaxPairingIdentities, 1024)
	pairSeededBackends(t, 1023, boundedOutcome, peer.PairingRejected, func(t *testing.T, db peer.PairingStore) {
		last := boundedOutcome(1024)
		pairEqual(t, pairSaved(t, db, last).State, peer.PairingPending)
		_, err := db.Reject(context.Background(), last.IdentityKey)
		pairOK(t, err)
		pairCapacityRefusal(t, db, boundedOutcome(1025), peer.ErrIdentityCapacity)
		first := boundedOutcome(1)
		pairEqual(t, pairSaved(t, db, first).State, peer.PairingRejected)
		known := boundedOutcome(1026)
		known.IdentityKey = first.IdentityKey
		pairEqual(t, pairSaved(t, db, known).State, peer.PairingRejected)
		rows, err := db.Requests(context.Background())
		pairOK(t, err)
		pairEqual(t, len(rows), 1024)
	})
}

func TestPairingBoundsAcceptanceOutcomeLimitRetainsReplayAndConflicts(t *testing.T) {
	pairEqual(t, peer.MaxPairingOutcomes, 4096)
	first := boundedOutcome(1)
	outcome := func(n int) peer.PairingOutcome {
		o := boundedOutcome(n)
		o.IdentityKey = first.IdentityKey
		return o
	}
	pairSeededBackends(t, 4095, outcome, peer.PairingPending, func(t *testing.T, db peer.PairingStore) {
		pairEqual(t, pairSaved(t, db, outcome(4096)).IdentityKey, first.IdentityKey)
		blocked := boundedOutcome(4097)
		blocked.IdentityKey = first.IdentityKey
		pairCapacityRefusal(t, db, blocked, peer.ErrOutcomeCapacity)
		pairEqual(t, pairSaved(t, db, first).IdentityKey, first.IdentityKey)
		changed := first
		changed.Name = "changed"
		_, err := db.Commit(context.Background(), changed)
		pairEqual(t, errors.Is(err, peer.ErrOutcomeConflict), true)
		pairEqual(t, pairReceipt(t, db, first.OperationID).Name, first.Name)
	})
}

func TestPairingBoundsAcceptanceLegacyReceiptReplaysWithoutRewriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db := pairSQLite(t, path)
	raw := pairRaw(t, path)
	legacy := boundedOutcome(1)
	legacy.IdentityKey = "old-provisional-key"
	legacy.Name = strings.Repeat("legacy name ", 20)
	legacy.TranscriptID = bytes.Repeat([]byte{1}, 100)
	_, err := raw.Exec(`INSERT INTO peer_pairing_requests(identity_key,name,state,first_paired_at) VALUES(?,?,?,?);`, legacy.IdentityKey, legacy.Name, "rejected", legacy.At.Format("2006-01-02T15:04:05.999999999Z07:00"))
	pairOK(t, err)
	_, err = raw.Exec(`INSERT INTO peer_pairing_outcomes(operation_id,invitation_id,identity_key,name,transcript_id,at) VALUES(?,?,?,?,?,?)`, legacy.OperationID, legacy.InvitationID, legacy.IdentityKey, legacy.Name, legacy.TranscriptID, legacy.At.Format("2006-01-02T15:04:05.999999999Z07:00"))
	pairOK(t, err)
	pairEqual(t, pairSaved(t, db, legacy).State, peer.PairingRejected)
	pairEqual(t, pairReceipt(t, db, legacy.OperationID).Name, legacy.Name)
	changed := legacy
	changed.Name = "changed"
	_, err = db.Commit(context.Background(), changed)
	pairEqual(t, errors.Is(err, peer.ErrOutcomeConflict), true)
	_, err = db.Reject(context.Background(), legacy.IdentityKey)
	pairOK(t, err)
	newOp := legacy
	newOp.OperationID = "new-operation"
	newOp.InvitationID = "new-invitation"
	_, err = db.Commit(context.Background(), newOp)
	if err == nil {
		t.Fatal("legacy identity admitted a newly malformed outcome")
	}
	pairMissingReceipt(t, db, newOp.OperationID)
}
