package store_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

var messageAt = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

func messageContact(t *testing.T, db *store.Store, n int) string {
	t.Helper()
	o := pairOutcome(n)
	o.IdentityKey = joinKey(n + 10)
	pairSaved(t, db, o)
	_, err := db.Accept(context.Background(), o.IdentityKey)
	pairOK(t, err)
	_, err = db.Deliver(context.Background(), o.IdentityKey, peer.PairingAccepted, messageAt)
	pairOK(t, err)
	return o.IdentityKey
}

func messageFixture(t *testing.T) (*store.Store, string) {
	t.Helper()
	db := pairSQLite(t, filepath.Join(t.TempDir(), "messages.db"))
	return db, messageContact(t, db, 1)
}

func messagePage(t *testing.T, s messaging.Store, contact string) []messaging.Row {
	t.Helper()
	rows, _, err := s.Page(context.Background(), contact, 0, 1000)
	pairOK(t, err)
	return rows
}

func messageWire(id string, n uint64) messaging.Message {
	return messaging.Message{ID: id, N: n, CreatedAt: messageAt, Text: "text for " + id}
}

func TestPeerMessagesAcceptanceOnlyConfirmedContacts(t *testing.T) {
	db := pairSQLite(t, filepath.Join(t.TempDir(), "messages.db"))
	s := db.PeerMessages()
	o := pairOutcome(1)
	pairSaved(t, db, o)
	for _, phase := range []string{"pending", "accepted-unconfirmed"} {
		t.Run(phase, func(t *testing.T) {
			if phase == "accepted-unconfirmed" {
				_, err := db.Accept(context.Background(), o.IdentityKey)
				pairOK(t, err)
			}
			_, err := s.Queue(context.Background(), messaging.Outgoing{Contact: o.IdentityKey, ID: "out", Text: "text", Now: messageAt})
			pairEqual(t, errors.Is(err, messaging.ErrUnknownContact), true)
			_, err = s.Accept(context.Background(), o.IdentityKey, messageWire("in", 1), messageAt)
			pairEqual(t, errors.Is(err, messaging.ErrUnknownContact), true)
		})
	}
}

func TestPeerMessagesAcceptanceQueueReplayAndSequenceSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.db")
	db := pairSQLite(t, path)
	contact := messageContact(t, db, 1)
	s := db.PeerMessages()
	first, err := s.Queue(context.Background(), messaging.Outgoing{Contact: contact, ID: "first", Text: "hello", Now: messageAt})
	pairOK(t, err)
	pairEqual(t, first.N, uint64(1))
	repeat, err := s.Queue(context.Background(), messaging.Outgoing{Contact: contact, ID: "first", Text: "hello", Now: messageAt.Add(time.Hour)})
	pairOK(t, err)
	pairEqual(t, repeat.N, first.N)
	pairEqual(t, repeat.Cursor, first.Cursor)
	pairEqual(t, repeat.CreatedAt.Equal(first.CreatedAt), true)
	_, err = s.Queue(context.Background(), messaging.Outgoing{Contact: contact, ID: "first", Text: "different", Now: messageAt})
	pairEqual(t, errors.Is(err, messaging.ErrConflict), true)
	pairOK(t, db.Close())
	reopened := pairSQLite(t, path)
	next, err := reopened.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: contact, ID: "second", Text: "hello", Now: messageAt})
	pairOK(t, err)
	pairEqual(t, next.N, uint64(2))
	pairEqual(t, len(messagePage(t, reopened.PeerMessages(), contact)), 2)
}

func TestPeerMessagesAcceptanceReplayPrecedesFullInboxQuota(t *testing.T) {
	db, contact := messageFixture(t)
	s := db.PeerMessages()
	ctx := context.Background()
	for i := 1; i <= messaging.UnreadMessages; i++ {
		answer, err := s.Accept(ctx, contact, messageWire(fmt.Sprintf("in-%d", i), uint64(i)), messageAt)
		pairOK(t, err)
		pairEqual(t, answer.Stored, true)
	}
	first := messageWire("in-1", 1)
	repeat, err := s.Accept(ctx, contact, first, messageAt.Add(time.Hour))
	pairOK(t, err)
	pairEqual(t, repeat.Stored, true)
	overflow, err := s.Accept(ctx, contact, messageWire("overflow", 257), messageAt)
	pairOK(t, err)
	pairEqual(t, overflow.Refusal, messaging.Refuse(messaging.ReasonInboxFull))
	changed := first
	changed.CreatedAt = changed.CreatedAt.Add(time.Nanosecond)
	conflict, err := s.Accept(ctx, contact, changed, messageAt)
	pairOK(t, err)
	pairEqual(t, conflict.Refusal, messaging.Refuse(messaging.ReasonConflict))
	marked, err := s.MarkRead(ctx, contact, []string{first.ID}, messageAt)
	pairOK(t, err)
	pairEqual(t, marked, 1)
	retry, err := s.Accept(ctx, contact, messageWire("overflow", 257), messageAt)
	pairOK(t, err)
	pairEqual(t, retry.Stored, true)
}

func TestPeerMessagesAcceptanceSameSequenceCannotChangeIdentity(t *testing.T) {
	db, contact := messageFixture(t)
	s := db.PeerMessages()
	answer, err := s.Accept(context.Background(), contact, messageWire("one", 1), messageAt)
	pairOK(t, err)
	pairEqual(t, answer.Stored, true)
	answer, err = s.Accept(context.Background(), contact, messageWire("other", 1), messageAt)
	pairOK(t, err)
	pairEqual(t, answer.Refusal, messaging.Refuse(messaging.ReasonConflict))
	pairEqual(t, len(messagePage(t, s, contact)), 1)
}

func TestPeerMessagesAcceptanceOutboxQuotaDedupAndRefusal(t *testing.T) {
	db, contact := messageFixture(t)
	s := db.PeerMessages()
	ctx := context.Background()
	for i := 0; i < messaging.OutboxMessages; i++ {
		_, err := s.Queue(ctx, messaging.Outgoing{Contact: contact, ID: fmt.Sprintf("out-%d", i), Text: "text", Now: messageAt})
		pairOK(t, err)
	}
	_, err := s.Queue(ctx, messaging.Outgoing{Contact: contact, ID: "out-0", Text: "text", Now: messageAt})
	pairOK(t, err)
	_, err = s.Queue(ctx, messaging.Outgoing{Contact: contact, ID: "overflow", Text: "text", Now: messageAt})
	pairEqual(t, errors.Is(err, messaging.ErrOutboxFull), true)
	pairOK(t, s.Refused(ctx, contact, messaging.Refused{ID: "out-0", Refusal: messaging.Refuse(messaging.ReasonBusy)}))
	_, err = s.Queue(ctx, messaging.Outgoing{Contact: contact, ID: "overflow", Text: "text", Now: messageAt})
	pairEqual(t, errors.Is(err, messaging.ErrOutboxFull), true)
	pairOK(t, s.Refused(ctx, contact, messaging.Refused{ID: "out-0", Refusal: messaging.Refuse(messaging.ReasonInvalid)}))
	row, err := s.Queue(ctx, messaging.Outgoing{Contact: contact, ID: "overflow", Text: "text", Now: messageAt})
	pairOK(t, err)
	pairEqual(t, row.N, uint64(65))
}

func TestPeerMessagesAcceptanceReadReceiptsAreDirectionalAndDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.db")
	db := pairSQLite(t, path)
	contact := messageContact(t, db, 1)
	other := messageContact(t, db, 2)
	s := db.PeerMessages()
	ctx := context.Background()
	_, err := s.Queue(ctx, messaging.Outgoing{Contact: contact, ID: "out", Text: "outbound", Now: messageAt})
	pairOK(t, err)
	_, err = s.Accept(ctx, contact, messageWire("in", 1), messageAt)
	pairOK(t, err)
	count, err := s.MarkRead(ctx, contact, []string{"in", "in", "out", "missing"}, messageAt)
	pairOK(t, err)
	pairEqual(t, count, 1)
	receipts, err := s.PendingReads(ctx, contact, 10)
	pairOK(t, err)
	pairEqual(t, len(receipts), 1)
	pairEqual(t, receipts[0], "in")
	pairOK(t, s.ReadConfirmed(ctx, other, "in"))
	pairOK(t, s.PeerRead(ctx, other, "out", messageAt))
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	s = db.PeerMessages()
	receipts, err = s.PendingReads(ctx, contact, 10)
	pairOK(t, err)
	pairEqual(t, len(receipts), 1)
	pairOK(t, s.ReadConfirmed(ctx, contact, "in"))
	receipts, err = s.PendingReads(ctx, contact, 10)
	pairOK(t, err)
	pairEqual(t, len(receipts), 0)
	pairOK(t, s.PeerRead(ctx, contact, "out", messageAt.Add(time.Minute)))
	pairOK(t, s.Delivered(ctx, contact, "out", messageAt.Add(time.Hour)))
	for _, row := range messagePage(t, s, contact) {
		if row.ID == "out" {
			pairEqual(t, row.State, messaging.StateRead)
			if row.ReadAt == nil || row.DeliveredAt == nil {
				t.Fatal("read must also establish delivery")
			}
		}
	}
}

func TestPeerMessagesAcceptanceExpiredQueueCanReceiveLateDelivery(t *testing.T) {
	db, contact := messageFixture(t)
	s := db.PeerMessages()
	ctx := context.Background()
	_, err := s.Queue(ctx, messaging.Outgoing{Contact: contact, ID: "old", Text: "text", Now: messageAt})
	pairOK(t, err)
	rows, err := s.Offer(ctx, contact, messageAt.Add(messaging.QueuedExpiry+time.Second), messaging.Limit{Count: 16, Bytes: 512 << 10})
	pairOK(t, err)
	pairEqual(t, len(rows), 0)
	pairEqual(t, messagePage(t, s, contact)[0].State, messaging.StateExpired)
	pairOK(t, s.Delivered(ctx, contact, "old", messageAt.Add(8*24*time.Hour)))
	pairEqual(t, messagePage(t, s, contact)[0].State, messaging.StateDelivered)
}

func TestPeerMessagesAcceptancePageUsesLocalCursorAcrossDirections(t *testing.T) {
	db, contact := messageFixture(t)
	s := db.PeerMessages()
	ctx := context.Background()
	_, err := s.Queue(ctx, messaging.Outgoing{Contact: contact, ID: "out", Text: "outbound", Now: messageAt})
	pairOK(t, err)
	_, err = s.Accept(ctx, contact, messageWire("in", 1), messageAt)
	pairOK(t, err)
	page, more, err := s.Page(ctx, contact, 0, 1)
	pairOK(t, err)
	pairEqual(t, len(page), 1)
	pairEqual(t, more, true)
	pairEqual(t, page[0].ID, "in")
	older, more, err := s.Page(ctx, contact, page[0].Cursor, 1)
	pairOK(t, err)
	pairEqual(t, len(older), 1)
	pairEqual(t, more, false)
	pairEqual(t, older[0].ID, "out")
	if older[0].Cursor >= page[0].Cursor {
		t.Fatal("pagination did not advance local insertion cursor")
	}
}
