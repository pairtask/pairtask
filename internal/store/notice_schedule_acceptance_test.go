package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func TestNoticeScheduleAcceptanceFiveTotalAttemptsAndNoSixthDue(t *testing.T) {
	f, n := noticeScheduleFixture(t)
	pairEqual(t, n.Attempts, 0)
	pairEqual(t, n.State, "paused")
	for attempt := 1; attempt <= 5; attempt++ {
		at := messageAt.Add(time.Duration(attempt) * time.Hour)
		next := at.Add(time.Minute)
		n = noticeAttempt(t, f, store.NoticeAttempt{Contact: f.contact, Gen: n.Gen, Outcome: store.NoticeFailed, At: at, Next: next})
		pairEqual(t, n.Attempts, attempt)
		due, err := f.db.PeerMessages().PendingNotices(context.Background(), at)
		pairOK(t, err)
		pairEqual(t, len(due), 0)
		due, err = f.db.PeerMessages().PendingNotices(context.Background(), next)
		pairOK(t, err)
		if attempt < 5 {
			pairEqual(t, len(due), 1)
		} else {
			pairEqual(t, len(due), 0)
		}
	}
	pairEqual(t, noticeDeliveryState(t, f, n.Gen), "undelivered")
	due, err := f.db.PeerMessages().PendingNotices(context.Background(), messageAt.Add(30*24*time.Hour))
	pairOK(t, err)
	pairEqual(t, len(due), 0)
}
func TestNoticeScheduleAcceptanceLostAckStillSpendsAttempt(t *testing.T) {
	f, n := noticeScheduleFixture(t)
	n = noticeAttempt(t, f, store.NoticeAttempt{Contact: f.contact, Gen: n.Gen, Outcome: store.NoticeSent, At: messageAt, Next: messageAt.Add(time.Minute)})
	pairEqual(t, n.Attempts, 1)
	pairEqual(t, noticeDeliveryState(t, f, n.Gen), "pending")
	c, err := f.db.PeerMessages().AdmitControl(context.Background(), f.contact)
	pairOK(t, err)
	pairOK(t, c.NoticeStored(context.Background(), messaging.ContactStateStored{PairingID: n.PairingID, Gen: uint64(n.Gen)}))
	n = noticeAttempt(t, f, store.NoticeAttempt{Contact: f.contact, Gen: n.Gen, Outcome: store.NoticeSent, At: messageAt.Add(time.Minute), Next: messageAt.Add(2 * time.Minute)})
	pairEqual(t, n.Attempts, 2)
	pairEqual(t, noticeDeliveryState(t, f, n.Gen), "delivered")
	due, err := f.db.PeerMessages().PendingNotices(context.Background(), messageAt.Add(time.Hour))
	pairOK(t, err)
	pairEqual(t, len(due), 0)
}
func TestNoticeScheduleAcceptanceReplacementDoesNotSpendAnotherGeneration(t *testing.T) {
	f, old := noticeScheduleFixture(t)
	pauseApply(t, f, pauseRequest(t, f, "notice-resume", false))
	fresh := noticeDueOne(t, f, messageAt)
	pairEqual(t, fresh.State, "resumed")
	pairEqual(t, fresh.Gen > old.Gen, true)
	pairEqual(t, fresh.Attempts, 0)
	pairEqual(t, noticeDeliveryState(t, f, old.Gen), "superseded")
	stale := noticeAttempt(t, f, store.NoticeAttempt{Contact: f.contact, Gen: old.Gen, Outcome: store.NoticeFailed, At: messageAt, Next: messageAt.Add(time.Hour)})
	pairEqual(t, stale.Attempts, 1)
	pairEqual(t, noticeDeliveryState(t, f, old.Gen), "superseded")
	fresh = noticeDueOne(t, f, messageAt)
	pairEqual(t, fresh.Attempts, 0)
}
func TestNoticeScheduleAcceptanceNewestDeliveredEvidenceIsRetained(t *testing.T) {
	f, first := noticeScheduleFixture(t)
	c, err := f.db.PeerMessages().AdmitControl(context.Background(), f.contact)
	pairOK(t, err)
	pairOK(t, c.NoticeStored(context.Background(), messaging.ContactStateStored{PairingID: first.PairingID, Gen: uint64(first.Gen)}))
	pauseApply(t, f, pauseRequest(t, f, "evidence-resume", false))
	second := noticeDueOne(t, f, messageAt)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM contact_notices_out WHERE state='delivered'`), 1)
	c, err = f.db.PeerMessages().AdmitControl(context.Background(), f.contact)
	pairOK(t, err)
	pairOK(t, c.NoticeStored(context.Background(), messaging.ContactStateStored{PairingID: second.PairingID, Gen: uint64(second.Gen)}))
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM contact_notices_out WHERE state='delivered'`), 1)
	pairEqual(t, noticeDeliveryState(t, f, second.Gen), "delivered")
}

func TestNoticeScheduleAcceptanceLegacyNotifiabilityUsesObservedVersion(t *testing.T) {
	for _, version := range []byte{0, 8, 9} {
		f := newOperatorContactFixture(t)
		if version != 0 {
			bound, err := f.db.PeerMessages().Admit(context.Background(), f.contact)
			pairOK(t, err)
			pairOK(t, bound.SawVersion(context.Background(), version))
		}
		pauseApply(t, f, pauseRequest(t, f, "legacy-notice-pause", true))
		rows, err := f.db.PeerMessages().PendingNotices(context.Background(), messageAt)
		pairOK(t, err)
		want := 0
		if version == 9 {
			want = 1
		}
		pairEqual(t, len(rows), want)
	}
}
func TestNoticeScheduleAcceptanceLocalOperationsStayLocalOnBoundStore(t *testing.T) {
	f, n := noticeScheduleFixture(t)
	// Pause then resume locally to permit ordinary admission again.
	pauseApply(t, f, pauseRequest(t, f, "bound-notice-resume", false))
	bound, err := f.db.PeerMessages().Admit(context.Background(), f.contact)
	pairOK(t, err)
	_, err = bound.PendingNotices(context.Background(), messageAt)
	pairEqual(t, errors.Is(err, store.ErrFenced), true)
	_, err = bound.RecordNoticeAttempt(context.Background(), store.NoticeAttempt{Contact: f.contact, Gen: n.Gen, Outcome: store.NoticeFailed, At: messageAt})
	pairEqual(t, errors.Is(err, store.ErrFenced), true)
}
