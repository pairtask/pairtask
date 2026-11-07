package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
)

func TestInboxExportAcceptanceFullPrivateSnapshotLeavesSourceUnchanged(t *testing.T) {
	f := newOfflineInbox(t)
	f.seed(t, offlineSeed{id: "received"})
	f.seed(t, offlineSeed{id: "unfinished", pending: "operator"})
	before := offlineSnapshot(t, f.database)
	code, output, stderr := f.call(t, "export")
	assertStatusEqual(t, stderr, code, 0)
	result := offlineResult(t, output)
	assertStatusEqual(t, "export destination", result["path"], any(f.output))
	data, err := os.ReadFile(f.output)
	offlineOK(t, err)
	assertStatusEqual(t, "published file hash", result["sha256"], any(fmt.Sprintf("%x", sha256.Sum256(data))))
	assertStatusEqual(t, "published file size", result["bytes"], any(float64(len(data))))
	info, err := os.Stat(f.output)
	offlineOK(t, err)
	assertStatusEqual(t, "private backup", info.Mode().Perm(), os.FileMode(0600))
	assertStatusEqual(t, "every table restored", offlineSnapshot(t, f.output), before)
	assertStatusEqual(t, "source unchanged", offlineSnapshot(t, f.database), before)
}

func TestInboxExportAcceptanceIncludesCommittedWALWithoutCheckpoint(t *testing.T) {
	f := newOfflineInbox(t)
	f.seed(t, offlineSeed{id: "before-wal"})
	raw := offlineSQL(t, f.database, false)
	_, err := raw.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE wal_proof (value TEXT); INSERT INTO wal_proof VALUES ('committed-in-wal')`)
	offlineOK(t, err)
	info, err := os.Stat(f.database + "-wal")
	offlineOK(t, err)
	if info.Size() == 0 {
		t.Fatal("fixture did not retain a WAL")
	}
	before := offlineSnapshot(t, f.database)
	code, _, stderr := f.call(t, "export")
	assertStatusEqual(t, stderr, code, 0)
	assertStatusEqual(t, "WAL snapshot content", offlineSnapshot(t, f.output), before)
}

func TestInboxExportAcceptanceNeverOverwritesExistingDestination(t *testing.T) {
	f := newOfflineInbox(t)
	f.seed(t, offlineSeed{id: "private"})
	offlineOK(t, os.WriteFile(f.output, []byte("keep-existing"), 0600))
	before := offlineSnapshot(t, f.database)
	code, _, _ := f.call(t, "export")
	if code == 0 {
		t.Fatal("export overwrote an existing destination")
	}
	data, err := os.ReadFile(f.output)
	offlineOK(t, err)
	assertStatusEqual(t, "existing destination kept", string(data), "keep-existing")
	assertStatusEqual(t, "failed export source", offlineSnapshot(t, f.database), before)
}
