package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func refusedNotice(t *testing.T, s messaging.Store, contact, id string, n uint64, at time.Time) messaging.Message {
	t.Helper()
	m := remoteAsk(id)
	m.N = n
	a, err := s.Accept(context.Background(), contact, m, at)
	pairOK(t, err)
	pairEqual(t, a.Stored, false)
	pairEqual(t, a.Refusal, messaging.Refuse(messaging.ReasonNotShared))
	pairEqual(t, a.Recorded, true)
	return m
}

func TestRefusalVisibilityBoundsAcceptanceReopenAndLocalRead(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "refused.db")
	db := pairSQLite(t, path)
	contact := messageContact(t, db, 1)
	m := refusedNotice(t, db.PeerMessages(), contact, "refused", 1, messageAt)
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	s := db.PeerMessages()
	rows := messagePage(t, s, contact)
	pairEqual(t, len(rows), 1)
	r := rows[0]
	pairEqual(t, r.Direction, messaging.DirIn)
	pairEqual(t, r.State, messaging.StateRefused)
	pairEqual(t, r.ID, m.ID)
	pairEqual(t, r.N, m.N)
	pairEqual(t, r.CreatedAt.Equal(m.CreatedAt), true)
	pairEqual(t, r.From, m.From)
	if !reflect.DeepEqual(r.To, m.To) || r.Review == nil || *r.Review != *m.Review {
		t.Fatalf("refusal lost recipients or pinned ask: %+v", r)
	}
	pairEqual(t, r.Text, "")
	pairEqual(t, r.ReadAt == nil, true)
	pairEqual(t, r.Refusal, messaging.Refuse(messaging.ReasonNotShared))
	other := messageContact(t, db, 2)
	n, err := s.MarkRead(ctx, other, []string{m.ID}, messageAt)
	pairOK(t, err)
	pairEqual(t, n, 0)
	readAt := messageAt.Add(time.Hour)
	n, err = s.MarkRead(ctx, contact, []string{m.ID, m.ID, "missing"}, readAt)
	pairOK(t, err)
	pairEqual(t, n, 1)
	n, err = s.MarkRead(ctx, contact, []string{m.ID}, readAt.Add(time.Hour))
	pairOK(t, err)
	pairEqual(t, n, 0)
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	s = db.PeerMessages()
	r = messagePage(t, s, contact)[0]
	pairEqual(t, r.State, messaging.StateRefused)
	if r.ReadAt == nil || !r.ReadAt.Equal(readAt) {
		t.Fatalf("read time not preserved: %+v", r.ReadAt)
	}
	pairEqual(t, r.Receipt, "")
	pending, err := s.PendingReads(ctx, contact, 10)
	pairOK(t, err)
	pairEqual(t, len(pending), 0)
}

func TestRefusalVisibilityBoundsAcceptanceUnreadCapAndSlotRecovery(t *testing.T) {
	pairEqual(t, messaging.RefusedNotices, 32)
	db, contact := messageFixture(t)
	s, ctx := db.PeerMessages(), context.Background()
	for i := 1; i <= 32; i++ {
		refusedNotice(t, s, contact, fmt.Sprintf("refused-%d", i), uint64(i), messageAt)
	}
	pairEqual(t, len(messagePage(t, s, contact)), 32)
	m := remoteAsk("overflow")
	m.N = 33
	a, err := s.Accept(ctx, contact, m, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Refusal, messaging.Refuse(messaging.ReasonInboxFull))
	pairEqual(t, a.Recorded, false)
	pairEqual(t, len(messagePage(t, s, contact)), 32)
	// Bounds are per contact, and normal operator messages keep their own quota.
	other := messageContact(t, db, 2)
	refusedNotice(t, s, other, "other-contact", 1, messageAt)
	a, err = s.Accept(ctx, contact, messageWire("operator-message", 34), messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	n, err := s.MarkRead(ctx, contact, []string{"refused-1"}, messageAt)
	pairOK(t, err)
	pairEqual(t, n, 1)
	refusedNotice(t, s, contact, m.ID, m.N, messageAt)
	pairEqual(t, len(messagePage(t, s, contact)), 34)
}

func TestRefusalVisibilityBoundsAcceptanceTotalTombstoneCap(t *testing.T) {
	db, raw, contact := messageRawFixture(t)
	s, ctx := db.PeerMessages(), context.Background()
	_, err := s.Accept(ctx, contact, messageWire("original", 1), messageAt)
	pairOK(t, err)
	_, err = s.MarkRead(ctx, contact, []string{"original"}, messageAt)
	pairOK(t, err)
	pairOK(t, s.ReadConfirmed(ctx, contact, "original"))
	messageSeedSettled(t, raw, contact, messaging.Tombstones-1)
	refusedNotice(t, s, contact, "last-slot", uint64(messaging.Tombstones), messageAt)
	m := remoteAsk("over-total-cap")
	m.N = uint64(messaging.Tombstones + 1)
	a, err := s.Accept(ctx, contact, m, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Refusal, messaging.Refuse(messaging.ReasonInboxFull))
	var count int
	pairOK(t, raw.QueryRow("SELECT COUNT(*) FROM peer_messages WHERE contact=? AND direction='in'", contact).Scan(&count))
	pairEqual(t, count, messaging.Tombstones)
}

func TestRefusalVisibilityBoundsAcceptanceRetentionAndReplayFloor(t *testing.T) {
	db, contact := messageFixture(t)
	s, ctx := db.PeerMessages(), context.Background()
	read := refusedNotice(t, s, contact, "read-refusal", 1, messageAt)
	refusedNotice(t, s, contact, "unread-refusal", 2, messageAt)
	_, err := s.MarkRead(ctx, contact, []string{read.ID}, messageAt)
	pairOK(t, err)
	later := messageAt.Add(messaging.TombstoneAge + time.Hour)
	pairOK(t, s.Collect(ctx, later))
	rows := messagePage(t, s, contact)
	pairEqual(t, len(rows), 1)
	pairEqual(t, rows[0].ID, "unread-refusal")
	pairEqual(t, rows[0].ReadAt == nil, true)
	for _, old := range []messaging.Message{read, messageWire("changed-old-id", 1)} {
		a, err := s.Accept(ctx, contact, old, later)
		pairOK(t, err)
		pairEqual(t, a.Refusal, messaging.Refuse(messaging.ReasonTooOld))
	}
	refusedNotice(t, s, contact, "fresh-refusal", 3, later)
}

func TestRefusalVisibilityBoundsAcceptanceUnreadLowerSequenceProtectsFloor(t *testing.T) {
	db, contact := messageFixture(t)
	s, ctx := db.PeerMessages(), context.Background()
	refusedNotice(t, s, contact, "higher-read", 2, messageAt)
	_, err := s.MarkRead(ctx, contact, []string{"higher-read"}, messageAt)
	pairOK(t, err)
	refusedNotice(t, s, contact, "lower-unread", 1, messageAt.Add(time.Hour))
	pairOK(t, s.Collect(ctx, messageAt.Add(messaging.TombstoneAge+2*time.Hour)))
	pairEqual(t, len(messagePage(t, s, contact)), 2)
}

func TestRefusalVisibilityBoundsAcceptanceInvalidAsksLeaveNoNotice(t *testing.T) {
	for _, scenario := range []string{"zero-sequence", "invalid-id", "invalid-recipient", "missing-pin", "empty-text", "invalid-utf8", "oversize"} {
		t.Run(scenario, func(t *testing.T) {
			db, contact := messageFixture(t)
			m := remoteAsk("invalid-notice")
			reason := messaging.ReasonInvalid
			switch scenario {
			case "zero-sequence":
				m.N = 0
			case "invalid-id":
				m.ID = "invalid id"
			case "invalid-recipient":
				m.To = []string{"operator"}
			case "missing-pin":
				m.Review.Head = ""
			case "empty-text":
				m.Text = ""
			case "invalid-utf8":
				m.Text = string([]byte{0xff})
			case "oversize":
				m.Text = strings.Repeat("x", messaging.TextBytes+1)
				reason = messaging.ReasonTooLarge
			}
			a, err := db.PeerMessages().Accept(context.Background(), contact, m, messageAt)
			pairOK(t, err)
			pairEqual(t, a.Stored, false)
			pairEqual(t, a.Recorded, false)
			pairEqual(t, a.Refusal, messaging.Refuse(reason))
			pairEqual(t, len(messagePage(t, db.PeerMessages(), contact)), 0)
		})
	}
}

func TestRefusalVisibilityBoundsAcceptanceBelowFloorAskLeavesNoNotice(t *testing.T) {
	db, contact := messageFixture(t)
	s, ctx := db.PeerMessages(), context.Background()
	_, err := s.Accept(ctx, contact, messageWire("old", 1), messageAt)
	pairOK(t, err)
	_, err = s.MarkRead(ctx, contact, []string{"old"}, messageAt)
	pairOK(t, err)
	pairOK(t, s.ReadConfirmed(ctx, contact, "old"))
	later := messageAt.Add(messaging.TombstoneAge + time.Hour)
	pairOK(t, s.Collect(ctx, later))
	pairEqual(t, len(messagePage(t, s, contact)), 0)
	a, err := s.Accept(ctx, contact, remoteAsk("below-floor"), later)
	pairOK(t, err)
	pairEqual(t, a.Stored, false)
	pairEqual(t, a.Recorded, false)
	pairEqual(t, a.Refusal, messaging.Refuse(messaging.ReasonTooOld))
	pairEqual(t, len(messagePage(t, s, contact)), 0)
}

func TestRefusalVisibilityBoundsAcceptanceKeepsOriginalHashesWithoutText(t *testing.T) {
	db, raw, contact := messageRawFixture(t)
	m := refusedNotice(t, db.PeerMessages(), contact, "hash-only", 1, messageAt)
	var hash, textHash []byte
	var textBytes int
	pairOK(t, raw.QueryRow("SELECT hash,text_hash,text_bytes FROM peer_messages WHERE contact=? AND message_id=?", contact, m.ID).Scan(&hash, &textHash, &textBytes))
	wantHash, wantTextHash := m.Hash(), sha256.Sum256([]byte(m.Text))
	if !bytes.Equal(hash, wantHash[:]) || !bytes.Equal(textHash, wantTextHash[:]) {
		t.Fatal("notice lost original replay identity")
	}
	pairEqual(t, textBytes, 0)
}
