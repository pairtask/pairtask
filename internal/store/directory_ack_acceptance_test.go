package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/store"
)

func directoryReceiptState(t *testing.T, f workspaceStoreFixture) (bool, *time.Time) {
	t.Helper()
	snap, err := f.db.PeerMessages().SharedSnapshot(context.Background(), f.key)
	pairOK(t, err)
	pairEqual(t, snap.Digest, store.DirectoryDigest(snap.Agents))
	return snap.Confirmed(), snap.AckAt
}

func directoryRecordReceipt(t *testing.T, f workspaceStoreFixture, agents []string, at time.Time) bool {
	t.Helper()
	changed, err := f.db.PeerMessages().DirectoryAcked(context.Background(), f.key, agents, at)
	pairOK(t, err)
	return changed
}

func TestDirectoryAckAcceptanceDigestUsesCanonicalLengthFraming(t *testing.T) {
	agents := []string{"b", "a", "a"}
	before := slices.Clone(agents)
	pairEqual(t, store.DirectoryDigest(agents), "3c9d591045bc8876f9d0399bbfb05c6a412096e906f73278f98406cd5dca86df")
	pairEqual(t, slices.Equal(agents, before), true)
	pairEqual(t, store.DirectoryDigest([]string{"a\x00b"}), "a360553fcb4ddf01f4b43108f585b3e3a55b1bd4d40478488ef112cce31ea2b1")
	pairEqual(t, store.DirectoryDigest(nil), "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	pairEqual(t, store.DirectoryDigest(nil), store.DirectoryDigest([]string{}))
}

func TestDirectoryAckAcceptanceUnknownAndAcknowledgedEmptySetAreDistinct(t *testing.T) {
	db, raw, key := messageRawFixture(t)
	f := workspaceStoreFixture{db, raw, key}
	confirmed, at := directoryReceiptState(t, f)
	pairEqual(t, confirmed, false)
	pairEqual(t, at == nil, true)
	var digest sql.NullString
	pairOK(t, raw.QueryRow(`SELECT directory_ack FROM peer_contacts WHERE identity_key=?`, key).Scan(&digest))
	pairEqual(t, digest.Valid, false)
	pairEqual(t, directoryRecordReceipt(t, f, nil, messageAt), true)
	confirmed, at = directoryReceiptState(t, f)
	pairEqual(t, confirmed, true)
	if at == nil || !at.Equal(messageAt) {
		t.Fatal("empty-set acknowledgment lost its receipt time")
	}
	pairOK(t, raw.QueryRow(`SELECT directory_ack FROM peer_contacts WHERE identity_key=?`, key).Scan(&digest))
	pairEqual(t, digest.String, store.DirectoryDigest(nil))
}

func TestDirectoryAckAcceptanceRepeatedReceiptDoesNotWriteOrChangeTime(t *testing.T) {
	f := workspaceStore(t)
	pairEqual(t, directoryRecordReceipt(t, f, []string{"sender"}, messageAt), true)
	_, err := f.raw.Exec(`CREATE TRIGGER forbid_ack_write BEFORE UPDATE OF directory_ack ON peer_contacts BEGIN SELECT RAISE(ABORT,'duplicate receipt wrote again'); END`)
	pairOK(t, err)
	pairEqual(t, directoryRecordReceipt(t, f, []string{"sender", "sender"}, messageAt.Add(time.Hour)), false)
	confirmed, at := directoryReceiptState(t, f)
	pairEqual(t, confirmed, true)
	if at == nil || !at.Equal(messageAt) {
		t.Fatal("identical receipt changed the evidence timestamp")
	}
}

func TestDirectoryAckAcceptanceOlderRoundCannotConfirmChangedGrantSet(t *testing.T) {
	f := workspaceStore(t)
	pairEqual(t, directoryRecordReceipt(t, f, []string{"sender"}, messageAt), true)
	routingRegister(t, f.db, "second")
	_, err := f.db.PeerMessages().Share(context.Background(), f.key, []string{"second"}, messageAt.Add(time.Minute))
	pairOK(t, err)
	// An earlier round finishes after the change, acknowledging only sender.
	pairEqual(t, directoryRecordReceipt(t, f, []string{"sender"}, messageAt.Add(2*time.Minute)), false)
	confirmed, _ := directoryReceiptState(t, f)
	pairEqual(t, confirmed, false)
	pairEqual(t, directoryRecordReceipt(t, f, []string{"second", "sender"}, messageAt.Add(3*time.Minute)), true)
	confirmed, _ = directoryReceiptState(t, f)
	pairEqual(t, confirmed, true)
	_, err = f.db.PeerMessages().Unshare(context.Background(), f.key, []string{"sender", "second"}, messageAt.Add(4*time.Minute))
	pairOK(t, err)
	confirmed, _ = directoryReceiptState(t, f)
	pairEqual(t, confirmed, false)
	pairEqual(t, directoryRecordReceipt(t, f, nil, messageAt.Add(5*time.Minute)), true)
	confirmed, _ = directoryReceiptState(t, f)
	pairEqual(t, confirmed, true)
}

func TestDirectoryAckAcceptanceReceiptIsBoundToContactAndSurvivesReopen(t *testing.T) {
	f := workspaceStore(t)
	other := messageContact(t, f.db, 2)
	_, err := f.db.PeerMessages().Share(context.Background(), other, []string{"sender"}, messageAt)
	pairOK(t, err)
	pairEqual(t, directoryRecordReceipt(t, f, []string{"sender"}, messageAt), true)
	second := f
	second.key = other
	confirmed, at := directoryReceiptState(t, second)
	pairEqual(t, confirmed, false)
	pairEqual(t, at == nil, true)
	var seq int
	var name, path string
	pairOK(t, f.raw.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path))
	pairOK(t, f.db.Close())
	reopened, err := store.Open(path)
	pairOK(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	f.db = reopened
	confirmed, at = directoryReceiptState(t, f)
	pairEqual(t, confirmed, true)
	if at == nil || !at.Equal(messageAt) {
		t.Fatal("persisted receipt did not survive reopen")
	}
}

func TestDirectoryAckAcceptanceWriteFailureNeverReportsProgress(t *testing.T) {
	f := workspaceStore(t)
	_, err := f.raw.Exec(`CREATE TRIGGER fail_ack_write BEFORE UPDATE OF directory_ack ON peer_contacts BEGIN SELECT RAISE(ABORT,'injected receipt failure'); END`)
	pairOK(t, err)
	changed, err := f.db.PeerMessages().DirectoryAcked(context.Background(), f.key, []string{"sender"}, messageAt)
	if err == nil || changed {
		t.Fatal("failed receipt write reported directory progress")
	}
	confirmed, at := directoryReceiptState(t, f)
	pairEqual(t, confirmed, false)
	pairEqual(t, at == nil, true)
}

func TestDirectoryAckAcceptanceCommitFailureNeverReportsProgress(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "receipt.db") + "?_pragma=foreign_keys(1)"
	db, err := store.Open(dsn)
	pairOK(t, err)
	t.Cleanup(func() { _ = db.Close() })
	raw, err := sql.Open("sqlite", dsn)
	pairOK(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	f := workspaceStoreFixture{db, raw, messageContact(t, db, 1)}
	_, err = raw.Exec(`CREATE TABLE ack_parent(id INTEGER PRIMARY KEY);
CREATE TABLE ack_child(id INTEGER REFERENCES ack_parent(id) DEFERRABLE INITIALLY DEFERRED);
CREATE TRIGGER fail_ack_commit AFTER UPDATE OF directory_ack ON peer_contacts BEGIN INSERT INTO ack_child VALUES(99); END`)
	pairOK(t, err)
	changed, err := db.PeerMessages().DirectoryAcked(context.Background(), f.key, nil, messageAt)
	if err == nil {
		t.Fatal("deferred receipt commit failure was not injected")
	}
	if changed {
		t.Fatal("rolled-back receipt commit reported directory progress")
	}
	confirmed, at := directoryReceiptState(t, f)
	pairEqual(t, confirmed, false)
	pairEqual(t, at == nil, true)
}
