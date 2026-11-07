package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func TestInboxArchiveAcceptanceBackupPrecedesReclamationAndUpperIsInclusive(t *testing.T) {
	f := newOfflineInbox(t)
	f.seed(t, offlineSeed{id: "first"})
	f.seed(t, offlineSeed{id: "at-upper"})
	f.seed(t, offlineSeed{id: "later"})
	before := offlineSnapshot(t, f.database)
	_, charged := f.retained(t)
	result := f.archive(t, "--upper", "2")
	offlineArchiveMetadata(t, f, result)
	assertStatusEqual(t, "backup is the complete pre-archive database", offlineSnapshot(t, f.output), before)
	assertStatusEqual(t, "upper frozen", offlineJSONCount(t, result, "upper"), int64(2))
	freed := offlineMap(t, result["archived"])
	assertStatusEqual(t, "both inclusive rows archived", offlineJSONCount(t, freed, "messages"), int64(2))
	count, remaining := f.retained(t)
	assertStatusEqual(t, "retained capacity reclaimed", count, int64(1))
	assertStatusEqual(t, "charged bytes reported", offlineJSONCount(t, freed, "bytes"), charged-remaining)
	usage := offlineMap(t, result["retained"])
	offlineUsageReport(t, usage["before"], offlineUsage{3, charged})
	offlineUsageReport(t, usage["after"], offlineUsage{count, remaining})
	assertStatusEqual(t, "message limit reported", offlineJSONCount(t, usage, "limit_messages"), int64(store.MaxRetainedMessages))
	assertStatusEqual(t, "byte limit reported", offlineJSONCount(t, usage, "limit_bytes"), int64(store.MaxRetainedBytes))
	f.archived(t, "first")
	f.archived(t, "at-upper")
	assertStatusEqual(t, "newer payload preserved", f.payload(t, "later"), `{"text":"original-body-later"}`)
	assertStatusEqual(t, "all message IDs reserved", offlineCount(t, f.database, `SELECT COUNT(*) FROM message_metadata`), int64(3))
}

func TestInboxArchiveAcceptanceCapacityCanBeUsedAgainWithoutSequenceReuse(t *testing.T) {
	f := newOfflineInbox(t)
	f.seed(t, offlineSeed{id: "fills-retained-bytes"})
	f.exec(t, `UPDATE durable_messages SET storage_bytes=?`, store.MaxRetainedBytes)
	fresh := offlineSeed{id: "after-archive"}.message(2)
	recipients := offlineSeed{}.recipients()
	err := f.db.PersistMessage(context.Background(), fresh, "author-session", recipients)
	assertStatusEqual(t, "fixture reaches actual quota check", errors.Is(err, store.ErrInboxFull), true)
	f.archive(t)
	restarted := f.open(t)
	seq, err := restarted.LastSeq(context.Background())
	offlineOK(t, err)
	assertStatusEqual(t, "old high-water survives", seq, uint64(1))
	offlineOK(t, restarted.PersistMessage(context.Background(), fresh, "author-session", recipients))
	seq, err = restarted.LastSeq(context.Background())
	offlineOK(t, err)
	assertStatusEqual(t, "new message follows archived high-water", seq, uint64(2))
}

func TestInboxArchiveAcceptancePruneFailureRollsBackAndKeepsCompletedBackup(t *testing.T) {
	f := newOfflineInbox(t)
	f.seed(t, offlineSeed{id: "rollback-original"})
	f.exec(t, `CREATE TRIGGER fail_archive BEFORE UPDATE OF payload ON messages BEGIN SELECT RAISE(ABORT,'fixture prune failure'); END`)
	before := offlineSnapshot(t, f.database)
	code, _, _ := f.call(t, "archive", "--confirm")
	if code == 0 {
		t.Fatal("archive claimed success after pruning failed")
	}
	assertStatusEqual(t, "transaction rollback preserves source", offlineSnapshot(t, f.database), before)
	_, err := os.Stat(f.output)
	offlineOK(t, err)
	assertStatusEqual(t, "completed recovery backup retained", offlineSnapshot(t, f.output), before)
}

func TestInboxArchiveAcceptanceDryRunReportsPlanWithoutWriting(t *testing.T) {
	f := newOfflineInbox(t)
	f.seed(t, offlineSeed{id: "would-archive"})
	f.seed(t, offlineSeed{id: "pending", pending: protocol.OperatorID})
	before := offlineSnapshot(t, f.database)
	code, output, stderr := f.call(t, "archive", "--dry-run")
	assertStatusEqual(t, stderr, code, 0)
	result := offlineResult(t, output)
	assertStatusEqual(t, "dry-run explicit", result["dry_run"], any(true))
	assertStatusEqual(t, "no fabricated backup", result["backup"], any(nil))
	assertStatusEqual(t, "plan count", offlineJSONCount(t, offlineMap(t, result["archived"]), "messages"), int64(1))
	assertStatusEqual(t, "unacknowledged protected", offlineJSONCount(t, offlineMap(t, result["kept_by_reason"]), "unacknowledged"), int64(1))
	assertStatusEqual(t, "dry-run source unchanged", offlineSnapshot(t, f.database), before)
	_, err := os.Stat(f.output)
	assertStatusEqual(t, "dry-run writes no backup", os.IsNotExist(err), true)
}
