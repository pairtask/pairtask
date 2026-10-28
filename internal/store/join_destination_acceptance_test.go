package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestJoinDestinationAcceptanceImmutableOnReplay(t *testing.T) {
	joinBackends(t, func(t *testing.T, db peer.JoinStore) {
		ctx := context.Background()
		start := peer.JoinStart{OperationID: "destination", Joiner: joinKey(1), Name: "Bob", Destination: "colleague.example:443", At: joinAt}
		row, err := db.BeginJoin(ctx, start)
		pairOK(t, err)
		pairEqual(t, row.Destination, start.Destination)
		_, err = db.FailJoin(ctx, start.OperationID, joinAt)
		pairOK(t, err)
		row, err = db.BeginJoin(ctx, start)
		pairOK(t, err)
		pairEqual(t, row.State, peer.JoinFailed)
		changed := start
		changed.Destination = "attacker.example:443"
		_, err = db.BeginJoin(ctx, changed)
		pairEqual(t, errors.Is(err, peer.ErrJoinConflict), true)
		joinUnchanged(t, db, row)
	})
}

func TestJoinDestinationAcceptanceMigratesExistingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	pairOK(t, err)
	_, err = old.Exec(`CREATE TABLE peer_joins (
operation_id TEXT PRIMARY KEY, joiner TEXT NOT NULL, joiner_name TEXT NOT NULL,
invitation_id TEXT NOT NULL DEFAULT '', inviter_operation_id TEXT NOT NULL DEFAULT '',
peer TEXT NOT NULL DEFAULT '', transcript_id BLOB, state TEXT NOT NULL,
created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`)
	pairOK(t, err)
	_, err = old.Exec(`INSERT INTO peer_joins(operation_id,joiner,joiner_name,state,created_at,updated_at) VALUES(?,?,?,?,?,?)`, "legacy", joinKey(1), "Bob", "intent", joinAt.Format(time.RFC3339Nano), joinAt.Format(time.RFC3339Nano))
	pairOK(t, err)
	pairOK(t, old.Close())
	db := pairSQLite(t, path)
	legacy := joinGet(t, db, "legacy")
	pairEqual(t, legacy.Destination, "")
	pairEqual(t, legacy.State, peer.JoinIntent)
	_, err = db.BeginJoin(context.Background(), peer.JoinStart{OperationID: "legacy", Joiner: joinKey(1), Name: "Bob", At: joinAt})
	pairOK(t, err)
	row, err := db.BeginJoin(context.Background(), peer.JoinStart{OperationID: "new", Joiner: joinKey(1), Name: "Bob", Destination: "host:443", At: joinAt})
	pairOK(t, err)
	pairEqual(t, row.Destination, "host:443")
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	pairEqual(t, joinGet(t, db, "new").Destination, "host:443")
}
