package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func ackReceive(t *testing.T, s *store.PeerMessages, key string, message messaging.Message) {
	t.Helper()
	a, err := s.Accept(context.Background(), key, message, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
}

func TestContactAckStoreAcceptanceReadIsNotHumanAckAndSurvivesRetention(t *testing.T) {
	db, key := messageFixture(t)
	s, ctx := db.PeerMessages(), context.Background()
	ackReceive(t, s, key, messageWire("shown", 1))
	_, err := s.MarkRead(ctx, key, []string{"shown"}, messageAt)
	pairOK(t, err)
	pairOK(t, s.ReadConfirmed(ctx, key, "shown"))
	later := messageAt.Add(365 * 24 * time.Hour)
	pairOK(t, s.Collect(ctx, later))
	rows := messagePage(t, s, key)
	pairEqual(t, len(rows), 1)
	pairEqual(t, rows[0].Text, "text for shown")
	pairEqual(t, rows[0].AckState, "needs_ack")
	pairEqual(t, rows[0].AckedAt == nil, true)
	n, summary, err := s.Ack(ctx, key, store.AckSelector{IDs: []string{"shown"}}, messageAt)
	pairOK(t, err)
	pairEqual(t, n, 1)
	pairEqual(t, summary.Unacked, 0)
	pairOK(t, s.Collect(ctx, later))
	pairEqual(t, len(messagePage(t, s, key)), 0)
}

func TestContactAckStoreAcceptanceContactDirectionAndIdempotency(t *testing.T) {
	db, key := messageFixture(t)
	other := messageContact(t, db, 2)
	s, ctx := db.PeerMessages(), context.Background()
	ackReceive(t, s, key, messageWire("same", 1))
	ackReceive(t, s, other, messageWire("same", 1))
	_, err := s.Queue(ctx, messaging.Outgoing{Contact: key, ID: "same", Text: "out", Now: messageAt})
	pairOK(t, err)
	for i := 0; i < 2; i++ {
		n, _, err := s.Ack(ctx, key, store.AckSelector{IDs: []string{"same", "same", "unknown"}}, messageAt.Add(time.Duration(i)*time.Hour))
		pairOK(t, err)
		pairEqual(t, n, 1-i)
	}
	for _, row := range messagePage(t, s, key) {
		if row.Direction == "out" {
			pairEqual(t, row.AckState, "")
			pairEqual(t, row.AckedAt == nil, true)
			continue
		}
		pairEqual(t, row.AckState, "acked")
		pairEqual(t, row.AckedAt.Equal(messageAt), true)
		pairEqual(t, row.State, messaging.StateReceived)
		pairEqual(t, row.ReadAt == nil, true)
	}
	pairEqual(t, messagePage(t, s, other)[0].AckState, "needs_ack")
	reads, err := s.PendingReads(ctx, key, 64)
	pairOK(t, err)
	pairEqual(t, len(reads), 0)
}

func TestContactAckStoreAcceptanceBulkBoundIncludesUnloadedAndExcludesNew(t *testing.T) {
	db, key := messageFixture(t)
	s, ctx := db.PeerMessages(), context.Background()
	for i := 1; i <= 80; i++ {
		ackReceive(t, s, key, messageWire(fmt.Sprint("in-", i), uint64(i)))
	}
	frozen, err := s.AckSummary(ctx, key)
	pairOK(t, err)
	pairEqual(t, frozen.Unacked, 80)
	ackReceive(t, s, key, messageWire("new", 81))
	n, after, err := s.Ack(ctx, key, store.AckSelector{Upto: frozen.AckUpto}, messageAt)
	pairOK(t, err)
	pairEqual(t, n, 80)
	pairEqual(t, after.Unacked, 1)
	n, after, err = s.Ack(ctx, key, store.AckSelector{Upto: frozen.AckUpto}, messageAt.Add(time.Hour))
	pairOK(t, err)
	pairEqual(t, n, 0)
	pairEqual(t, after.Unacked, 1)
	_, _, err = s.Ack(ctx, key, store.AckSelector{Upto: after.AckUpto + 1}, messageAt)
	pairEqual(t, errors.Is(err, store.ErrAckBound), true)
}

func TestContactAckStoreAcceptanceFailedUpdateIsAtomic(t *testing.T) {
	db, raw, key := messageRawFixture(t)
	s, ctx := db.PeerMessages(), context.Background()
	ackReceive(t, s, key, messageWire("first", 1))
	ackReceive(t, s, key, messageWire("second", 2))
	_, err := raw.Exec(`CREATE TRIGGER fail_ack BEFORE UPDATE OF acked_at ON peer_messages WHEN NEW.message_id='second' BEGIN SELECT RAISE(ABORT,'injected ACK failure'); END`)
	pairOK(t, err)
	_, _, err = s.Ack(ctx, key, store.AckSelector{IDs: []string{"first", "second"}}, messageAt)
	if err == nil {
		t.Fatal("ACK succeeded despite storage failure")
	}
	for _, row := range messagePage(t, s, key) {
		pairEqual(t, row.AckState, "needs_ack")
	}
}

func TestContactAckStoreAcceptanceMigrationFloorIsWrittenOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db := pairSQLite(t, path)
	key := messageContact(t, db, 1)
	ackReceive(t, db.PeerMessages(), key, messageWire("historical", 1))
	pairOK(t, db.Close())
	raw, err := sql.Open("sqlite", path)
	pairOK(t, err)
	for _, statement := range []string{"DROP TABLE peer_ack_floors", "ALTER TABLE peer_messages DROP COLUMN acked_at", "ALTER TABLE peer_messages DROP COLUMN ack_expired_at"} {
		_, err = raw.Exec(statement)
		pairOK(t, err)
	}
	pairOK(t, raw.Close())
	db = pairSQLite(t, path)
	pairEqual(t, messagePage(t, db.PeerMessages(), key)[0].AckState, "before")
	n, summary, err := db.PeerMessages().Ack(context.Background(), key, store.AckSelector{IDs: []string{"historical"}}, messageAt)
	pairOK(t, err)
	pairEqual(t, n, 0)
	pairEqual(t, summary.Unacked, 0)
	ackReceive(t, db.PeerMessages(), key, messageWire("tracked", 2))
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	summary, err = db.PeerMessages().AckSummary(context.Background(), key)
	pairOK(t, err)
	pairEqual(t, summary.Unacked, 1)
	other := messageContact(t, db, 2)
	ackReceive(t, db.PeerMessages(), other, messageWire("new-contact", 1))
	pairEqual(t, messagePage(t, db.PeerMessages(), other)[0].AckState, "needs_ack")
}

func TestContactAckStoreAcceptanceExpiryIsVisibleDurableAndNeverAcked(t *testing.T) {
	db, raw, key := messageRawFixture(t)
	s, ctx := db.PeerMessages(), context.Background()
	for i := 1; i <= protocol.UnackedKept+3; i++ {
		id := fmt.Sprint("in-", i)
		ackReceive(t, s, key, messageWire(id, uint64(i)))
		_, err := s.MarkRead(ctx, key, []string{id}, messageAt)
		pairOK(t, err)
		pairOK(t, s.ReadConfirmed(ctx, key, id))
	}
	summary, err := s.AckSummary(ctx, key)
	pairOK(t, err)
	pairEqual(t, summary.Unacked, protocol.UnackedKept)
	pairEqual(t, summary.AckExpired, 3)
	ackReceive(t, s, key, messageWire("in-1", 1))
	replayed, err := s.AckSummary(ctx, key)
	pairOK(t, err)
	pairEqual(t, replayed.AckExpired, 3)
	var expired, falseAck int
	pairOK(t, raw.QueryRow(`SELECT COUNT(*), COUNT(acked_at) FROM peer_messages WHERE ack_expired_at IS NOT NULL`).Scan(&expired, &falseAck))
	pairEqual(t, expired, 3)
	pairEqual(t, falseAck, 0)
	n, _, err := s.Ack(ctx, key, store.AckSelector{IDs: []string{"in-1", "in-2", "in-3"}}, messageAt)
	pairOK(t, err)
	pairEqual(t, n, 0)
	pairOK(t, s.Collect(ctx, messageAt.Add(365*24*time.Hour)))
	summary, err = s.AckSummary(ctx, key)
	pairOK(t, err)
	pairEqual(t, summary.AckExpired, 3)
	pairEqual(t, summary.Unacked, protocol.UnackedKept)
	pairOK(t, raw.QueryRow(`SELECT COUNT(*) FROM peer_messages WHERE ack_expired_at IS NOT NULL`).Scan(&expired))
	pairEqual(t, expired, 0)
}

func TestContactAckStoreAcceptanceExpiryFailureRollsBackNewArrival(t *testing.T) {
	db, raw, key := messageRawFixture(t)
	s, ctx := db.PeerMessages(), context.Background()
	ackReceive(t, s, key, messageWire("original", 1))
	_, err := s.MarkRead(ctx, key, []string{"original"}, messageAt)
	pairOK(t, err)
	messageSeedSettled(t, raw, key, protocol.UnackedKept)
	_, err = raw.Exec(`CREATE TRIGGER fail_expiry BEFORE UPDATE OF ack_expired_at ON peer_messages WHEN NEW.ack_expired_at IS NOT NULL BEGIN SELECT RAISE(ABORT,'injected expiry failure'); END`)
	pairOK(t, err)
	a, err := s.Accept(ctx, key, messageWire("new", protocol.UnackedKept+1), messageAt)
	if err == nil || a.Stored {
		t.Fatal("arrival reported stored despite failed transactional expiry")
	}
	var count int
	pairOK(t, raw.QueryRow(`SELECT COUNT(*) FROM peer_messages WHERE message_id='new'`).Scan(&count))
	pairEqual(t, count, 0)
	summary, err := s.AckSummary(ctx, key)
	pairOK(t, err)
	pairEqual(t, summary.Unacked, protocol.UnackedKept)
	pairEqual(t, summary.AckExpired, 0)
	_, err = raw.Exec(`DROP TRIGGER fail_expiry`)
	pairOK(t, err)
	ackReceive(t, s, key, messageWire("new", protocol.UnackedKept+1))
	summary, err = s.AckSummary(ctx, key)
	pairOK(t, err)
	pairEqual(t, summary.Unacked, protocol.UnackedKept)
	pairEqual(t, summary.AckExpired, 1)
}

func TestContactAckStoreAcceptanceAcknowledgmentSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acks.db")
	db := pairSQLite(t, path)
	key := messageContact(t, db, 1)
	ackReceive(t, db.PeerMessages(), key, messageWire("acked", 1))
	_, _, err := db.PeerMessages().Ack(context.Background(), key, store.AckSelector{IDs: []string{"acked"}}, messageAt)
	pairOK(t, err)
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	row := messagePage(t, db.PeerMessages(), key)[0]
	pairEqual(t, row.AckState, "acked")
	pairEqual(t, row.AckedAt != nil, true)
	pairEqual(t, row.AckedAt.Equal(messageAt), true)
	ackReceive(t, db.PeerMessages(), key, messageWire("new", 2))
	summary, err := db.PeerMessages().AckSummary(context.Background(), key)
	pairOK(t, err)
	pairEqual(t, summary.Unacked, 1)
}

func TestContactAckStoreAcceptanceFailedMigrationSeedRecoversHistoricalFloor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration-failure.db")
	db := pairSQLite(t, path)
	key := messageContact(t, db, 1)
	ackReceive(t, db.PeerMessages(), key, messageWire("historical", 1))
	pairOK(t, db.Close())
	raw, err := sql.Open("sqlite", path)
	pairOK(t, err)
	defer raw.Close()
	for _, statement := range []string{"DELETE FROM peer_ack_floors", "ALTER TABLE peer_messages DROP COLUMN acked_at", "ALTER TABLE peer_messages DROP COLUMN ack_expired_at", `CREATE TRIGGER fail_ack_seed BEFORE INSERT ON peer_ack_floors BEGIN SELECT RAISE(ABORT,'injected floor seed failure'); END`} {
		_, err = raw.Exec(statement)
		pairOK(t, err)
	}
	failed, err := store.Open(path)
	if err == nil {
		failed.Close()
		t.Fatal("migration ignored failed history floor seed")
	}
	_, err = raw.Exec("DROP TRIGGER fail_ack_seed")
	pairOK(t, err)
	db = pairSQLite(t, path)
	row := messagePage(t, db.PeerMessages(), key)[0]
	pairEqual(t, row.AckState, "before")
	pairEqual(t, row.AckedAt == nil, true)
	ackReceive(t, db.PeerMessages(), key, messageWire("new-after-recovery", 2))
	summary, err := db.PeerMessages().AckSummary(context.Background(), key)
	pairOK(t, err)
	pairEqual(t, summary.Unacked, 1)
}

func TestContactAckStoreAcceptanceExpiredSummaryOutlivesEveryMessage(t *testing.T) {
	db, raw, key := messageRawFixture(t)
	s, ctx := db.PeerMessages(), context.Background()
	ackReceive(t, s, key, messageWire("original", 1))
	_, err := s.MarkRead(ctx, key, []string{"original"}, messageAt)
	pairOK(t, err)
	pairOK(t, s.ReadConfirmed(ctx, key, "original"))
	messageSeedSettled(t, raw, key, protocol.UnackedKept)
	ackReceive(t, s, key, messageWire("overflow", protocol.UnackedKept+1))
	_, err = s.MarkRead(ctx, key, []string{"overflow"}, messageAt)
	pairOK(t, err)
	pairOK(t, s.ReadConfirmed(ctx, key, "overflow"))
	summary, err := s.AckSummary(ctx, key)
	pairOK(t, err)
	pairEqual(t, summary.AckExpired, 1)
	_, _, err = s.Ack(ctx, key, store.AckSelector{Upto: summary.AckUpto}, messageAt)
	pairOK(t, err)
	pairOK(t, s.Collect(ctx, messageAt.Add(365*24*time.Hour)))
	var count int
	pairOK(t, raw.QueryRow(`SELECT COUNT(*) FROM peer_messages WHERE contact=?`, key).Scan(&count))
	pairEqual(t, count, 0)
	summary, err = s.AckSummary(ctx, key)
	pairOK(t, err)
	pairEqual(t, summary.AckExpired, 1)
	all, err := s.AckSummaries(ctx)
	pairOK(t, err)
	pairEqual(t, all[key].AckExpired, 1)
	pairEqual(t, all[key].Unacked, 0)
}

func TestContactAckStoreAcceptancePendingHumanTextSurvivesCollectionBudget(t *testing.T) {
	db, raw, key := messageRawFixture(t)
	s, ctx := db.PeerMessages(), context.Background()
	ackReceive(t, s, key, messageWire("pending-human", 1))
	_, err := s.MarkRead(ctx, key, []string{"pending-human"}, messageAt)
	pairOK(t, err)
	messageSeedSettled(t, raw, key, messaging.RetainedText+1)
	_, err = raw.Exec(`UPDATE peer_messages SET acked_at=? WHERE message_id LIKE 'seed-%'`, messageAt.Format(time.RFC3339Nano))
	pairOK(t, err)
	pairOK(t, s.Collect(ctx, messageAt.Add(time.Hour)))
	var text sql.NullString
	pairOK(t, raw.QueryRow(`SELECT text FROM peer_messages WHERE message_id='pending-human'`).Scan(&text))
	pairEqual(t, text.Valid, true)
	pairEqual(t, text.String, "text for pending-human")
	summary, err := s.AckSummary(ctx, key)
	pairOK(t, err)
	pairEqual(t, summary.Unacked, 1)
}
