package hub

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func (f historyHubCase) checkFailureCounts(t *testing.T, version int, counts *protocol.ReviewCounts) {
	t.Helper()
	if counts == nil {
		t.Fatal("review counts missing")
	}
	page := f.h.pendingReviews(context.Background(), protocol.Message{Type: protocol.TypeOperatorReview, RequestID: "failed", ReviewOp: protocol.ReviewOpFailed, ProtocolVersion: version})
	messageSyncEqual(t, page.Type, protocol.TypeOperatorReviewResult)
	if page.ReviewFailed == nil {
		t.Fatal("failure page missing")
	}
	messageSyncEqual(t, counts.Failed, page.ReviewFailed.Total)
	messageSyncEqual(t, counts.FailedToken, page.ReviewFailed.Token)
	messageSyncEqual(t, counts.AskedOpen, 1)
	want := 0
	if version >= 25 {
		want = 1
	}
	messageSyncEqual(t, counts.Failed, want)
}

func TestContactHistoryCountsAcceptanceCountsOpMatchesVersionedFailurePage(t *testing.T) {
	for _, version := range []int{24, 25} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			f := historyHubAsks(t)
			out := f.h.reviewCounts(context.Background(), protocol.Message{Type: protocol.TypeOperatorReview, RequestID: "counts", ReviewOp: protocol.ReviewOpCounts, ProtocolVersion: version})
			messageSyncEqual(t, out.Type, protocol.TypeOperatorReviewResult)
			f.checkFailureCounts(t, version, out.ReviewCounts)
		})
	}
}

func TestContactHistoryCountsAcceptanceSnapshotMatchesNegotiatedFailurePage(t *testing.T) {
	for _, version := range []int{24, 25} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			f := historyHubAsks(t)
			sess := NewOperatorSession(0)
			opened := f.h.openSession(context.Background(), operatorRequest{msg: protocol.Message{Type: protocol.TypeOperatorSessionOpen, RequestID: "open", ProtocolVersion: version}, sess: sess})
			messageSyncEqual(t, opened.Type, protocol.TypeOperatorSession)
			f.h.routeSession(context.Background(), nil, operatorRequest{msg: protocol.Message{Type: protocol.TypeOperatorSnapshot, RequestID: "snapshot", ProtocolVersion: 25}, sess: sess})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			out, ok := sess.Next(ctx)
			messageSyncEqual(t, ok, true)
			messageSyncEqual(t, out.Type, protocol.TypeOperatorSnapshot)
			if out.Snapshot == nil {
				t.Fatal("operator snapshot missing")
			}
			f.checkFailureCounts(t, version, out.Snapshot.ReviewCounts)
		})
	}
}
