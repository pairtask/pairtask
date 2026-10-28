package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func TestPeerMessagesAcceptanceReplayFloorSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.db")
	db := pairSQLite(t, path)
	contact := messageContact(t, db, 1)
	s := db.PeerMessages()
	ctx := context.Background()
	old := messageWire("old", 1)
	_, err := s.Accept(ctx, contact, old, messageAt)
	pairOK(t, err)
	_, err = s.MarkRead(ctx, contact, []string{old.ID}, messageAt)
	pairOK(t, err)
	pairOK(t, s.ReadConfirmed(ctx, contact, old.ID))
	later := messageAt.Add(messaging.TombstoneAge + time.Hour)
	pairOK(t, s.Collect(ctx, later))
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	s = db.PeerMessages()
	for _, replay := range []messaging.Message{old, messageWire("different-id", 1)} {
		answer, err := s.Accept(ctx, contact, replay, later)
		pairOK(t, err)
		pairEqual(t, answer.Stored, false)
		pairEqual(t, answer.Refusal, messaging.Refuse(messaging.ReasonTooOld))
	}
	answer, err := s.Accept(ctx, contact, messageWire("next", 2), later)
	pairOK(t, err)
	pairEqual(t, answer.Stored, true)
}

func TestPeerMessagesAcceptanceFloorCannotJumpFreshLowerSequence(t *testing.T) {
	db, contact := messageFixture(t)
	s := db.PeerMessages()
	ctx := context.Background()
	// Arrival order differs from sender sequence. The higher sequence ages
	// first; collecting it must not make the fresh lower one too old.
	high := messageWire("high", 2)
	_, err := s.Accept(ctx, contact, high, messageAt)
	pairOK(t, err)
	_, err = s.MarkRead(ctx, contact, []string{high.ID}, messageAt)
	pairOK(t, err)
	freshAt := messageAt.Add(messaging.TombstoneAge - time.Hour)
	low := messageWire("low", 1)
	_, err = s.Accept(ctx, contact, low, freshAt)
	pairOK(t, err)
	_, err = s.MarkRead(ctx, contact, []string{low.ID}, freshAt)
	pairOK(t, err)
	now := messageAt.Add(messaging.TombstoneAge + time.Hour)
	pairOK(t, s.Collect(ctx, now))
	for _, retry := range []messaging.Message{low, high} {
		answer, err := s.Accept(ctx, contact, retry, now)
		pairOK(t, err)
		pairEqual(t, answer.Stored, true)
	}
}

func TestPeerMessagesAcceptanceUnreadTextNeverAgesOut(t *testing.T) {
	db, contact := messageFixture(t)
	s := db.PeerMessages()
	ctx := context.Background()
	unread := messageWire("unread", 1)
	_, err := s.Accept(ctx, contact, unread, messageAt)
	pairOK(t, err)
	now := messageAt.Add(365 * 24 * time.Hour)
	pairOK(t, s.Collect(ctx, now))
	rows := messagePage(t, s, contact)
	pairEqual(t, len(rows), 1)
	pairEqual(t, rows[0].Text, unread.Text)
	pairEqual(t, rows[0].State, messaging.StateReceived)
	answer, err := s.Accept(ctx, contact, unread, now)
	pairOK(t, err)
	pairEqual(t, answer.Stored, true)
}

func TestPeerMessagesAcceptanceReceiptExpiryDoesNotEraseText(t *testing.T) {
	db, contact := messageFixture(t)
	s := db.PeerMessages()
	ctx := context.Background()
	m := messageWire("read", 1)
	_, err := s.Accept(ctx, contact, m, messageAt)
	pairOK(t, err)
	readAt := messageAt.Add(time.Hour)
	_, err = s.MarkRead(ctx, contact, []string{m.ID}, readAt)
	pairOK(t, err)
	// Pending receipt has its own 30-day lifetime; tombstone expiry must
	// not silently eliminate the outstanding receipt earlier.
	pairOK(t, s.Collect(ctx, messageAt.Add(messaging.TombstoneAge+30*time.Minute)))
	pending, err := s.PendingReads(ctx, contact, 10)
	pairOK(t, err)
	pairEqual(t, len(pending), 1)
	pairOK(t, s.Collect(ctx, readAt.Add(messaging.ReceiptAge+time.Minute)))
	pending, err = s.PendingReads(ctx, contact, 10)
	pairOK(t, err)
	pairEqual(t, len(pending), 0)
	// Retention may collect an aged read row; if retained, its loss of
	// acknowledgment must be explicit rather than falsely confirmed.
	for _, row := range messagePage(t, s, contact) {
		if row.ID == m.ID {
			pairEqual(t, row.Receipt, messaging.ReceiptDropped)
		}
	}
}
