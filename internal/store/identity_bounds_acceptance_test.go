package store_test

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func TestIdentityOutboxReplacementPairing(t *testing.T) {
	ctx := context.Background()
	f := namespaceFixture(t)
	wires, _, _ := replayFixture(t)
	old := wireBound(t, f)
	queue := func() { pairOK(t, f.db.QueueIdentity(ctx, f.key, wires["anchor"], messageAt)) }
	pending := func(bound *store.PeerMessages) []messaging.IdentityStatement {
		v, err := bound.PendingIdentity(ctx, f.key, 2)
		pairOK(t, err)
		return v
	}
	queue()
	statements := pending(old)
	pairEqual(t, len(statements), 1)
	answer := messaging.IdentityStored{Digest: statements[0].Digest(), Effect: messaging.IdentityApplied}
	namespaceRepair(t, f)
	fresh := wireBound(t, f)
	pairEqual(t, len(pending(fresh)), 0)
	queue()
	rows, err := old.PendingIdentity(ctx, f.key, 2)
	pairEqual(t, errors.Is(err, store.ErrFenced), true)
	pairEqual(t, len(rows), 0)
	pairEqual(t, errors.Is(old.IdentityAnswered(ctx, f.key, answer), store.ErrFenced), true)
	pairEqual(t, len(pending(fresh)), 1)
	pairOK(t, fresh.IdentityAnswered(ctx, f.key, answer))
	pairEqual(t, len(pending(fresh)), 0)
}

// Seed the journal directly with signed records to avoid replaying the
// entire growing history 4094 times during setup. Reads and the final
// revocation still exercise the production replay and admission paths.
func fillIdentityJournal(t *testing.T, raw *sql.DB, record identity.Record) {
	t.Helper()
	_, person := admissionSigningKeys(t)
	tx, err := raw.Begin()
	pairOK(t, err)
	defer tx.Rollback()
	var count int
	lineage := hex.EncodeToString(record.Lineage[:])
	pairOK(t, tx.QueryRow("SELECT count(*) FROM identity_inputs WHERE lineage=?", lineage).Scan(&count))
	pairEqual(t, count, 2)
	insert, err := tx.Prepare("INSERT INTO identity_inputs(lineage,kind,body,digest,received_at) VALUES(?,'statement',?,?,?)")
	pairOK(t, err)
	defer insert.Close()
	for i := count; i < store.MaxIdentityInputs; i++ {
		record.Version = uint64(i)
		signed := record.Signed(person)
		_, err = insert.Exec(lineage, signed.Marshal(), identity.DigestOf(signed.TBS()).String(), messageAt.Format(time.RFC3339Nano))
		pairOK(t, err)
	}
	pairOK(t, tx.Commit())
}

func TestIdentityHistoryFullRevocationSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "full.db")
	db := pairSQLite(t, path)
	contact := messageContact(t, db, 1)
	bound, err := db.PeerMessages().Admit(ctx, contact)
	pairOK(t, err)
	wires, anchor, w := replayFixture(t)
	_, err = bound.ApplyIdentity(ctx, wires["anchor"], messageAt)
	pairOK(t, err)
	pairOK(t, db.ApproveIdentity(ctx, anchor.Lineage(), messageAt))
	pairOK(t, db.NoteWorkspaceKey(ctx, w, messageAt))
	record, err := identity.ParseRecord(wires["record"])
	pairOK(t, err)
	fillIdentityJournal(t, pairRaw(t, path), record)
	before, found, err := db.HeldIdentity(ctx, anchor.Lineage())
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, before.Trust, identity.Active)
	pairEqual(t, before.Version, uint64(store.MaxIdentityInputs-1))
	effect, err := bound.ApplyIdentity(ctx, wires["revocation"], messageAt)
	pairOK(t, err)
	pairEqual(t, effect.Kind, identity.Rejected)
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	after, found, err := db.HeldIdentity(ctx, anchor.Lineage())
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, after.Trust, identity.Frozen)
}
