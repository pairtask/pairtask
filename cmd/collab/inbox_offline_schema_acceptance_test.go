package main

import (
	"strings"
	"testing"
)

func offlinePremigration(t *testing.T, f *offlineInboxFixture) {
	t.Helper()
	offlineOK(t, f.db.Close())
	columns := offlineCount(t, f.database, `SELECT COUNT(*) FROM pragma_table_info('message_metadata') WHERE name='archived_at'`)
	if columns > 0 {
		f.exec(t, `ALTER TABLE message_metadata DROP COLUMN archived_at`)
	}
}

func TestInboxArchiveAcceptanceRefusesPremigrationWithoutMigratingSource(t *testing.T) {
	f := newOfflineInbox(t)
	f.seed(t, offlineSeed{id: "old-schema-original"})
	offlinePremigration(t, f)
	before := offlineSnapshot(t, f.database)
	code, _, stderr := f.call(t, "archive", "--confirm")
	if code == 0 {
		t.Fatal("archive silently migrated its source")
	}
	if !strings.Contains(strings.ToLower(stderr), "broker") {
		t.Fatalf("missing start-current-broker migration guidance: %s", stderr)
	}
	assertStatusEqual(t, "premigration source untouched", offlineSnapshot(t, f.database), before)
}

func TestInboxExportAcceptanceOlderSchemaNeedsNoMigration(t *testing.T) {
	f := newOfflineInbox(t)
	f.seed(t, offlineSeed{id: "old-schema-backup"})
	offlinePremigration(t, f)
	before := offlineSnapshot(t, f.database)
	code, _, stderr := f.call(t, "export")
	assertStatusEqual(t, stderr, code, 0)
	assertStatusEqual(t, "old source not migrated", offlineSnapshot(t, f.database), before)
	assertStatusEqual(t, "old schema backup complete", offlineSnapshot(t, f.output), before)
}
