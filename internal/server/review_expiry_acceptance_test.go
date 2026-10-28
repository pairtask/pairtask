//go:build linux

package server

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/store"
)

func reviewTestClockFixture(t *testing.T) (*operatorFixture, func(time.Duration)) {
	t.Helper()
	var clock atomic.Int64
	clock.Store(time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC).UnixNano())
	database := filepath.Join(t.TempDir(), "reviews.db")
	st, err := store.Open(database)
	operatorNoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	h := hub.New(st, 0, testLog())
	h.SetReviewClock(func() time.Time { return time.Unix(0, clock.Load()).UTC() })
	go h.Run(ctx)
	f := &operatorFixture{t: t, agent: socketPath(t), operator: socketPath(t)}
	f.db, err = sql.Open("sqlite", database)
	operatorNoError(t, err)
	a, o := New(f.agent, h, testLog()), NewOperator(f.operator, h, os.Getuid(), testLog())
	f.endpoint = o
	done := make(chan error, 2)
	go func() { done <- a.Listen(ctx) }()
	go func() { done <- o.Listen(ctx) }()
	t.Cleanup(func() {
		cancel()
		a.Close()
		o.Close()
		awaitOperatorShutdown(t, done, h.Done())
		f.db.Close()
		st.Close()
	})
	return f, func(d time.Duration) { clock.Add(int64(d)) }
}

func TestReviewAcceptanceExpiryPreventsLateStartAndCompletion(t *testing.T) {
	for _, state := range []string{"requested", "ready", "in_review"} {
		t.Run(state, func(t *testing.T) {
			f, advance := reviewTestClockFixture(t)
			p := reviewTestPeer(f, "alpha")
			reviewTestView(t, reviewTestRequest(f, "expiry", reviewTestSpec()), "operator_review_result")
			if state != "requested" {
				reviewTestView(t, reviewTestCall(p, "review_prepare", "expiry", map[string]any{"revisions": reviewTestRevisions()}), "review_result")
			}
			if state == "in_review" {
				reviewTestView(t, reviewTestCall(p, "review_accept", "expiry", nil), "review_result")
				advance(72*time.Hour + time.Second)
			} else {
				advance(24*time.Hour + time.Second)
			}
			kind := "review_accept"
			if state == "in_review" {
				kind = "review_submit"
			}
			operatorEqual(t, "expiry checked before mutation", reviewTestCall(p, kind, "expiry", map[string]any{"attempt": 1, "verdict": reviewTestVerdict()}).text("code"), "review_conflict")
			got := reviewTestView(t, reviewTestCall(p, "review_get", "expiry", nil), "review_result")
			operatorEqual(t, "expiry durable and visible", got.State, "expired")
			operatorEqual(t, "expired work has no result", got.Verdict == nil, true)
			operatorEqual(t, "expired work has no publication receipt", got.Publication.Receipt == nil, true)
		})
	}
}

func TestReviewAcceptanceCancelCompletedReviewPreservesPublicationTruth(t *testing.T) {
	for _, state := range []string{"pending", "submitting", "published"} {
		t.Run(state, func(t *testing.T) {
			f := newOperatorFixture(t, os.Getuid(), "")
			p := reviewTestPeer(f, "alpha")
			reviewTestComplete(f, p, "cancel-publication")
			intent := 0
			if state != "pending" {
				intent = reviewTestView(t, reviewTestPublication(p, "cancel-publication", "pending", "submitting", map[string]any{"token": "write"}), "review_result").Publication.Intent
			}
			if state == "published" {
				reviewTestView(t, reviewTestPublication(p, "cancel-publication", "submitting", "published", map[string]any{"intent": intent, "receipt": reviewTestReceipt()}), "review_result")
			}
			cancelled := reviewTestView(t, reviewTestOperator(f, map[string]any{"review_op": "cancel", "review_id": "cancel-publication", "review_reason": "stop"}), "operator_review_result")
			operatorEqual(t, "review cancelled", cancelled.State, "cancelled")
			operatorEqual(t, "saved verdict retained", cancelled.Verdict != nil, true)
			want := state
			if state == "pending" {
				want = "cancelled"
			}
			operatorEqual(t, "cancellation preserves write truth", cancelled.Publication.State, want)
			operatorEqual(t, "new write blocked", reviewTestPublication(p, "cancel-publication", want, "submitting", map[string]any{"token": "new-write"}).text("code"), "review_conflict")
			if state == "submitting" {
				reviewTestView(t, reviewTestPublication(p, "cancel-publication", "submitting", "uncertain", map[string]any{"intent": intent, "reason": "unavailable"}), "review_result")
				cancelled = reviewTestView(t, reviewTestPublication(p, "cancel-publication", "uncertain", "published", map[string]any{"intent": intent, "receipt": reviewTestReceipt()}), "review_result")
			}
			if state != "pending" {
				operatorEqual(t, "published review not retracted", *cancelled.Publication.Receipt, reviewTestReceipt())
				operatorEqual(t, "reconciliation does not uncancel task", cancelled.State, "cancelled")
			}
		})
	}
}
