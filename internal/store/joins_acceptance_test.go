package store_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

var joinAt = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

func joinKey(n int) string { return fmt.Sprintf("sha256:%064x", n) }
func joinBackends(t *testing.T, check func(*testing.T, peer.JoinStore)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) { check(t, peer.NewMemJoinStore()) })
	t.Run("sqlite", func(t *testing.T) { check(t, pairSQLite(t, filepath.Join(t.TempDir(), "joins.db"))) })
}
func joinGet(t *testing.T, db peer.JoinStore, id string) peer.JoinRecord {
	t.Helper()
	r, found, err := db.GetJoin(context.Background(), id)
	pairOK(t, err)
	pairEqual(t, found, true)
	return r
}
func joinStart(t *testing.T, db peer.JoinStore, id string) peer.JoinRecord {
	t.Helper()
	r, err := db.BeginJoin(context.Background(), id, joinKey(1), "Bob", joinAt)
	pairOK(t, err)
	return r
}
func joinVerified(t *testing.T, db peer.JoinStore, id string) peer.JoinRecord {
	t.Helper()
	joinStart(t, db, id)
	_, err := db.SetCorrelation(context.Background(), id, "inv-"+id, "remote-"+id, joinAt.Add(time.Second))
	pairOK(t, err)
	r, err := db.RecordVerified(context.Background(), id, joinKey(2), []byte("provider-transcript"), joinAt.Add(2*time.Second))
	pairOK(t, err)
	return r
}
func joinReceipt(r peer.JoinRecord) peer.JoinReceipt {
	return peer.JoinReceipt{InvitationID: r.InvitationID, InviterOperationID: r.InviterOperationID, Joiner: r.Joiner, TranscriptID: bytes.Clone(r.TranscriptID)}
}
func joinUnchanged(t *testing.T, db peer.JoinStore, before peer.JoinRecord) {
	t.Helper()
	pairEqual(t, reflect.DeepEqual(joinGet(t, db, before.OperationID), before), true)
}

func TestJoinsAcceptanceReceiptAndRetriesNeverRegress(t *testing.T) {
	joinBackends(t, func(t *testing.T, db peer.JoinStore) {
		ctx := context.Background()
		r := joinVerified(t, db, "one")
		pairEqual(t, r.State, peer.JoinOutcomeUnknown)
		later := joinAt.Add(time.Hour)
		_, err := db.RecordReceipt(ctx, r.OperationID, joinReceipt(r), later)
		pairOK(t, err)
		accepted := joinGet(t, db, r.OperationID)
		pairEqual(t, accepted.State, peer.JoinAwaitingAcceptance)
		pairEqual(t, accepted.Joiner, joinKey(1))
		pairEqual(t, accepted.Peer, joinKey(2))
		_, err = db.BeginJoin(ctx, r.OperationID, r.Joiner, r.JoinerName, later)
		pairOK(t, err)
		_, err = db.SetCorrelation(ctx, r.OperationID, r.InvitationID, r.InviterOperationID, later)
		pairOK(t, err)
		_, err = db.RecordVerified(ctx, r.OperationID, r.Peer, r.TranscriptID, later)
		pairOK(t, err)
		_, err = db.RecordReceipt(ctx, r.OperationID, joinReceipt(r), later.Add(time.Hour))
		pairOK(t, err)
		joinUnchanged(t, db, accepted)
		_, err = db.FailJoin(ctx, r.OperationID, later)
		pairEqual(t, errors.Is(err, peer.ErrJoinState), true)
		joinUnchanged(t, db, accepted)
	})
}

func TestJoinsAcceptanceEveryReceiptFieldMustMatch(t *testing.T) {
	mutations := map[string]func(*peer.JoinReceipt){
		"invitation": func(r *peer.JoinReceipt) { r.InvitationID += "x" },
		"operation":  func(r *peer.JoinReceipt) { r.InviterOperationID += "x" },
		"joiner":     func(r *peer.JoinReceipt) { r.Joiner = joinKey(3) },
		"transcript": func(r *peer.JoinReceipt) { r.TranscriptID[0] ^= 1 },
	}
	joinBackends(t, func(t *testing.T, db peer.JoinStore) {
		original := joinVerified(t, db, "one")
		for name, mutate := range mutations {
			t.Run(name, func(t *testing.T) {
				r := joinReceipt(original)
				mutate(&r)
				_, err := db.RecordReceipt(context.Background(), "one", r, joinAt.Add(time.Hour))
				pairEqual(t, errors.Is(err, peer.ErrJoinConflict), true)
				joinUnchanged(t, db, original)
			})
		}
	})
}

func TestJoinsAcceptanceImmutableCorrelationAndInputCopies(t *testing.T) {
	joinBackends(t, func(t *testing.T, db peer.JoinStore) {
		ctx := context.Background()
		r := joinVerified(t, db, "one")
		_, err := db.BeginJoin(ctx, "one", joinKey(3), "Bob", joinAt)
		pairEqual(t, errors.Is(err, peer.ErrJoinConflict), true)
		_, err = db.BeginJoin(ctx, "one", joinKey(1), "Mallory", joinAt)
		pairEqual(t, errors.Is(err, peer.ErrJoinConflict), true)
		_, err = db.SetCorrelation(ctx, "one", "changed", r.InviterOperationID, joinAt)
		pairEqual(t, errors.Is(err, peer.ErrJoinConflict), true)
		_, err = db.SetCorrelation(ctx, "one", r.InvitationID, "changed", joinAt)
		pairEqual(t, errors.Is(err, peer.ErrJoinConflict), true)
		_, err = db.RecordVerified(ctx, "one", joinKey(3), r.TranscriptID, joinAt)
		pairEqual(t, errors.Is(err, peer.ErrJoinConflict), true)
		_, err = db.RecordVerified(ctx, "one", r.Peer, []byte("changed"), joinAt)
		pairEqual(t, errors.Is(err, peer.ErrJoinConflict), true)
		joinUnchanged(t, db, r)
		copy := joinGet(t, db, "one")
		copy.TranscriptID[0] ^= 1
		rows, err := db.Joins(ctx)
		pairOK(t, err)
		rows[0].TranscriptID[0] ^= 1
		joinUnchanged(t, db, r)
	})
}

func TestJoinsAcceptanceRecoveryIsExplicitAndPreservesUnknown(t *testing.T) {
	joinBackends(t, func(t *testing.T, db peer.JoinStore) {
		ctx := context.Background()
		joinStart(t, db, "intent")
		joinStart(t, db, "exchange")
		_, err := db.SetCorrelation(ctx, "exchange", "inv", "remote", joinAt)
		pairOK(t, err)
		unknown := joinVerified(t, db, "unknown")
		ready := joinVerified(t, db, "ready")
		_, err = db.RecordReceipt(ctx, "ready", joinReceipt(ready), joinAt)
		pairOK(t, err)
		_, err = db.Joins(ctx)
		pairOK(t, err)
		pairEqual(t, joinGet(t, db, "intent").State, peer.JoinIntent)
		pairEqual(t, joinGet(t, db, "exchange").State, peer.JoinExchanging)
		n, err := db.RecoverJoins(ctx, joinAt.Add(time.Hour))
		pairOK(t, err)
		pairEqual(t, n, 2)
		pairEqual(t, joinGet(t, db, "intent").State, peer.JoinUnverified)
		pairEqual(t, joinGet(t, db, "exchange").State, peer.JoinUnverified)
		joinUnchanged(t, db, unknown)
		pairEqual(t, joinGet(t, db, "ready").State, peer.JoinAwaitingAcceptance)
		n, err = db.RecoverJoins(ctx, joinAt.Add(2*time.Hour))
		pairOK(t, err)
		pairEqual(t, n, 0)
		_, err = db.RecordVerified(ctx, "exchange", joinKey(2), []byte("late"), joinAt)
		pairEqual(t, errors.Is(err, peer.ErrJoinState), true)
	})
}

func TestJoinsAcceptanceOpenCapacityAndAtomicLastSlot(t *testing.T) {
	pairEqual(t, peer.MaxOpenJoins, 64)
	joinBackends(t, func(t *testing.T, db peer.JoinStore) {
		for n := range 63 {
			joinStart(t, db, fmt.Sprint(n))
		}
		var wg sync.WaitGroup
		results := make(chan error, 12)
		for n := range 12 {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				_, err := db.BeginJoin(context.Background(), fmt.Sprintf("race-%d", n), joinKey(1), "Bob", joinAt)
				results <- err
			}(n)
		}
		wg.Wait()
		close(results)
		winners := 0
		for err := range results {
			if err == nil {
				winners++
				continue
			}
			pairEqual(t, errors.Is(err, peer.ErrTooManyOpenJoins), true)
		}
		pairEqual(t, winners, 1)
		joinStart(t, db, "0") // Exact retries still work at capacity.
		_, err := db.FailJoin(context.Background(), "0", joinAt)
		pairOK(t, err)
		joinStart(t, db, "new-slot")
		rows, err := db.Joins(context.Background())
		pairOK(t, err)
		pairEqual(t, len(rows), 65)
	})
}

func TestJoinsAcceptanceInvalidNewFieldsWriteNothing(t *testing.T) {
	joinBackends(t, func(t *testing.T, db peer.JoinStore) {
		ctx := context.Background()
		for _, args := range [][3]string{{"", joinKey(1), "Bob"}, {strings.Repeat("x", 129), joinKey(1), "Bob"}, {"id", "bad", "Bob"}, {"id", joinKey(1), "bad\nname"}, {"id", joinKey(1), strings.Repeat("é", 33)}} {
			_, err := db.BeginJoin(ctx, args[0], args[1], args[2], joinAt)
			if err == nil {
				t.Fatal("invalid intent accepted")
			}
		}
		rows, err := db.Joins(ctx)
		pairOK(t, err)
		pairEqual(t, len(rows), 0)
		before := joinStart(t, db, "one")
		_, err = db.SetCorrelation(ctx, "one", "", "remote", joinAt)
		if err == nil {
			t.Fatal("empty correlation accepted")
		}
		joinUnchanged(t, db, before)
		_, err = db.RecordReceipt(ctx, "one", peer.JoinReceipt{}, joinAt)
		if err == nil {
			t.Fatal("unverified intent accepted a receipt")
		}
		joinUnchanged(t, db, before)
	})
}

func TestJoinsAcceptanceRestartRetainsReceiptCorrelation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	db := pairSQLite(t, path)
	r := joinVerified(t, db, "one")
	joinStart(t, db, "unverified")
	pairOK(t, db.Close())
	reopened := pairSQLite(t, path)
	joinUnchanged(t, reopened, r)
	_, err := reopened.RecoverJoins(context.Background(), joinAt.Add(time.Hour))
	pairOK(t, err)
	joinUnchanged(t, reopened, r)
	_, err = reopened.RecordReceipt(context.Background(), "one", joinReceipt(r), joinAt.Add(2*time.Hour))
	pairOK(t, err)
	pairEqual(t, joinGet(t, reopened, "one").State, peer.JoinAwaitingAcceptance)
}

func TestJoinsAcceptanceReceiptLookupIsExactAndReadOnly(t *testing.T) {
	pairBackends(t, func(t *testing.T, db peer.PairingStore) {
		o := boundedOutcome(1)
		pairSaved(t, db, o)
		r := peer.JoinReceipt{InvitationID: o.InvitationID, InviterOperationID: o.OperationID, Joiner: o.IdentityKey, TranscriptID: bytes.Clone(o.TranscriptID)}
		ok, err := peer.MatchReceipt(context.Background(), db, r)
		pairOK(t, err)
		pairEqual(t, ok, true)
		for _, mutate := range []func(*peer.JoinReceipt){
			func(r *peer.JoinReceipt) { r.InvitationID += "x" },
			func(r *peer.JoinReceipt) { r.InviterOperationID += "x" },
			func(r *peer.JoinReceipt) { r.Joiner = joinKey(4) },
			func(r *peer.JoinReceipt) { r.TranscriptID = []byte("wrong") },
		} {
			changed := r
			mutate(&changed)
			ok, err := peer.MatchReceipt(context.Background(), db, changed)
			pairOK(t, err)
			pairEqual(t, ok, false)
		}
		pairEqual(t, reflect.DeepEqual(pairReceipt(t, db, o.OperationID), o), true)
		rows, err := db.Requests(context.Background())
		pairOK(t, err)
		pairEqual(t, len(rows), 1)
	})
}

func joinTotalCheck(t *testing.T, db peer.JoinStore) {
	t.Helper()
	ctx := context.Background()
	joinStart(t, db, "last")
	_, err := db.FailJoin(ctx, "last", joinAt)
	pairOK(t, err)
	_, err = db.BeginJoin(ctx, "overflow", joinKey(1), "Bob", joinAt)
	pairEqual(t, errors.Is(err, peer.ErrJoinCapacity), true)
	_, found, err := db.GetJoin(ctx, "overflow")
	pairOK(t, err)
	pairEqual(t, found, false)
	first := joinGet(t, db, "0")
	joinStart(t, db, "0")
	joinUnchanged(t, db, first)
	rows, err := db.Joins(ctx)
	pairOK(t, err)
	pairEqual(t, len(rows), 1024)
}

func TestJoinsAcceptanceTotalCapacityRetainsTerminalRows(t *testing.T) {
	pairEqual(t, peer.MaxJoins, 1024)
	t.Run("memory", func(t *testing.T) {
		db := peer.NewMemJoinStore()
		for n := range 1023 {
			id := fmt.Sprint(n)
			joinStart(t, db, id)
			_, err := db.FailJoin(context.Background(), id, joinAt)
			pairOK(t, err)
		}
		joinTotalCheck(t, db)
	})
	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "capacity.db")
		db := pairSQLite(t, path)
		joinStart(t, db, "0")
		_, err := db.FailJoin(context.Background(), "0", joinAt)
		pairOK(t, err)
		tx, err := pairRaw(t, path).Begin()
		pairOK(t, err)
		defer tx.Rollback()
		// Bulk fixture only; the final slot and overflow use public operations.
		for n := 1; n < 1023; n++ {
			_, err := tx.Exec(`INSERT INTO peer_joins(operation_id,joiner,joiner_name,invitation_id,inviter_operation_id,peer,transcript_id,state,created_at,updated_at) SELECT ?,joiner,joiner_name,invitation_id,inviter_operation_id,peer,transcript_id,state,created_at,updated_at FROM peer_joins WHERE operation_id='0'`, fmt.Sprint(n))
			pairOK(t, err)
		}
		pairOK(t, tx.Commit())
		joinTotalCheck(t, db)
	})
}

func TestJoinsAcceptanceVerificationIsValidatedAndCopied(t *testing.T) {
	joinBackends(t, func(t *testing.T, db peer.JoinStore) {
		ctx := context.Background()
		joinStart(t, db, "one")
		before, err := db.SetCorrelation(ctx, "one", "inv", "remote", joinAt)
		pairOK(t, err)
		for _, transcript := range [][]byte{nil, {}, make([]byte, 65)} {
			_, err := db.RecordVerified(ctx, "one", joinKey(2), transcript, joinAt)
			if err == nil {
				t.Fatal("invalid transcript accepted")
			}
			joinUnchanged(t, db, before)
		}
		_, err = db.RecordVerified(ctx, "one", "invalid", []byte("ok"), joinAt)
		if err == nil {
			t.Fatal("invalid authenticated identity accepted")
		}
		joinUnchanged(t, db, before)
		transcript := bytes.Repeat([]byte{7}, 64)
		_, err = db.RecordVerified(ctx, "one", joinKey(2), transcript, joinAt)
		pairOK(t, err)
		transcript[0] = 99
		pairEqual(t, joinGet(t, db, "one").TranscriptID[0], byte(7))
	})
}

func TestJoinsAcceptanceVerificationWriteFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "failure.db")
	db := pairSQLite(t, path)
	joinStart(t, db, "one")
	before, err := db.SetCorrelation(ctx, "one", "inv", "remote", joinAt)
	pairOK(t, err)
	raw := pairRaw(t, path)
	_, err = raw.Exec(`CREATE TRIGGER reject_verified BEFORE UPDATE ON peer_joins WHEN NEW.state='outcome_unknown' BEGIN SELECT RAISE(ABORT, 'injected disk failure'); END`)
	pairOK(t, err)
	_, err = db.RecordVerified(ctx, "one", joinKey(2), []byte("transcript"), joinAt.Add(time.Second))
	if err == nil {
		t.Fatal("injected database failure was ignored")
	}
	joinUnchanged(t, db, before)
	_, err = raw.Exec(`DROP TRIGGER reject_verified`)
	pairOK(t, err)
	_, err = db.RecordVerified(ctx, "one", joinKey(2), []byte("transcript"), joinAt.Add(time.Second))
	pairOK(t, err)
	pairEqual(t, joinGet(t, db, "one").State, peer.JoinOutcomeUnknown)
}
