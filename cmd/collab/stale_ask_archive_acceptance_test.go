package main

import (
	"database/sql"
	"strings"
	"testing"
)

func staleArchiveSnapshot(t *testing.T, f *offlineInboxFixture) string {
	t.Helper()
	var hasColumn bool
	offlineOK(t, offlineSQL(t, f.database, true).QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('message_metadata') WHERE name='review_outcome')`).Scan(&hasColumn))
	if !hasColumn {
		f.exec(t, `ALTER TABLE message_metadata ADD COLUMN review_outcome TEXT`)
	}
	snapshot := `{"ask_id":"ask","reviewer":"reviewer","state":"declined","summary":"body-derived-text"}`
	f.exec(t, `UPDATE message_metadata SET review_outcome=? WHERE message_id='outcome'`, snapshot)
	return snapshot
}

func staleArchiveReadSnapshot(t *testing.T, f *offlineInboxFixture) sql.NullString {
	t.Helper()
	var snapshot sql.NullString
	offlineOK(t, offlineSQL(t, f.database, true).QueryRow(`SELECT review_outcome FROM message_metadata WHERE message_id='outcome'`).Scan(&snapshot))
	return snapshot
}

func TestStaleAskArchiveAcceptanceClearsSnapshotAndPreservesReceipt(t *testing.T) {
	f := newOfflineInbox(t)
	message := f.seed(t, offlineSeed{id: "outcome"})
	staleArchiveSnapshot(t, f)
	result := f.archive(t)
	f.archived(t, "outcome")
	assertStatusEqual(t, "snapshot cleared with body", staleArchiveReadSnapshot(t, f).Valid, false)
	assertStatusEqual(t, "freed retained count", offlineJSONCount(t, offlineMap(t, result["archived"]), "messages"), int64(1))
	var seq uint64
	var acknowledged bool
	offlineOK(t, offlineSQL(t, f.database, true).QueryRow(`SELECT mm.seq,di.acknowledged_at IS NOT NULL FROM message_metadata mm JOIN durable_inbox di USING(message_id) WHERE mm.message_id='outcome'`).Scan(&seq, &acknowledged))
	assertStatusEqual(t, "identity and sequence retained", seq, message.Seq)
	assertStatusEqual(t, "receipt retained", acknowledged, true)
}

func TestStaleAskArchiveAcceptanceRollbackPreservesSnapshot(t *testing.T) {
	f := newOfflineInbox(t)
	f.seed(t, offlineSeed{id: "outcome"})
	snapshot := staleArchiveSnapshot(t, f)
	original := f.payload(t, "outcome")
	f.exec(t, `CREATE TRIGGER stale_archive_failure BEFORE DELETE ON durable_messages BEGIN SELECT RAISE(ABORT,'injected archive failure'); END`)
	code, _, stderr := f.call(t, "archive", "--confirm")
	if code == 0 {
		t.Fatal("injected failure succeeded")
	}
	if !strings.Contains(stderr, "changed nothing") {
		t.Fatalf("missing rollback result: %s", stderr)
	}
	assertStatusEqual(t, "body rolled back", f.payload(t, "outcome"), original)
	assertStatusEqual(t, "snapshot rolled back", staleArchiveReadSnapshot(t, f).String, snapshot)
}

func TestStaleAskArchiveAcceptanceSupportsSchemaWithoutSnapshotColumn(t *testing.T) {
	f := newOfflineInbox(t)
	f.seed(t, offlineSeed{id: "outcome"})
	staleArchiveSnapshot(t, f)
	f.exec(t, `ALTER TABLE message_metadata DROP COLUMN review_outcome`)
	f.archive(t)
	f.archived(t, "outcome")
}
