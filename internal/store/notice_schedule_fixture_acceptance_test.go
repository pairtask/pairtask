package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/store"
)

func noticeScheduleFixture(t *testing.T) (*operatorContactFixture, store.NoticeDue) {
	t.Helper()
	f := installFixture(t)
	f.installedEpoch(t, 101)
	pauseApply(t, f, pauseRequest(t, f, "notice-pause", true))
	return f, noticeDueOne(t, f, messageAt)
}
func noticeDueOne(t *testing.T, f *operatorContactFixture, now time.Time) store.NoticeDue {
	t.Helper()
	due, err := f.db.PeerMessages().PendingNotices(context.Background(), now)
	pairOK(t, err)
	if len(due) != 1 {
		t.Fatalf("got %d due notices, want 1", len(due))
	}
	return due[0]
}
func noticeAttempt(t *testing.T, f *operatorContactFixture, a store.NoticeAttempt) store.NoticeDue {
	t.Helper()
	n, err := f.db.PeerMessages().RecordNoticeAttempt(context.Background(), a)
	pairOK(t, err)
	return n
}
func noticeDeliveryState(t *testing.T, f *operatorContactFixture, gen int64) string {
	t.Helper()
	var state string
	err := f.raw.QueryRow(`SELECT state FROM contact_notices_out WHERE contact=? AND gen=?`, f.contact, gen).Scan(&state)
	pairOK(t, err)
	return state
}
