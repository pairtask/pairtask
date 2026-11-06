package hub

import (
	"context"
	"strconv"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestContactHistoryOperatorAcceptanceLegacyAskedPageKeepsOnlyCurrent(t *testing.T) {
	for _, version := range []int{0, 24} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			f := historyHubAsks(t)
			out := f.h.pendingReviews(context.Background(), protocol.Message{Type: protocol.TypeOperatorReview, RequestID: "asked", ReviewOp: protocol.ReviewOpAsked, ProtocolVersion: version})
			messageSyncEqual(t, out.Type, protocol.TypeOperatorReviewResult)
			if out.ReviewAsked == nil {
				t.Fatal("asked page missing")
			}
			messageSyncEqual(t, out.ReviewAsked.Total, 1)
			messageSyncEqual(t, len(out.ReviewAsked.Items), 1)
			messageSyncEqual(t, out.ReviewAsked.Items[0].State, "asked")
			messageSyncEqual(t, out.ReviewAsked.Items[0].PairingEpoch, int64(0))
		})
	}
}

func TestContactHistoryOperatorAcceptanceLegacyFailedPageExcludesRetiredPairing(t *testing.T) {
	f := historyHubAsks(t)
	out := f.h.pendingReviews(context.Background(), protocol.Message{Type: protocol.TypeOperatorReview, RequestID: "failed", ReviewOp: protocol.ReviewOpFailed, ProtocolVersion: 24})
	messageSyncEqual(t, out.Type, protocol.TypeOperatorReviewResult)
	if out.ReviewFailed == nil {
		t.Fatal("failed page missing")
	}
	messageSyncEqual(t, out.ReviewFailed.Total, 0)
	messageSyncEqual(t, len(out.ReviewFailed.Items), 0)
	messageSyncEqual(t, out.ReviewFailed.Token, "")
}

func TestContactHistoryOperatorAcceptanceCapableReviewPagesRetainTaggedHistory(t *testing.T) {
	f := historyHubAsks(t)
	asked := f.h.pendingReviews(context.Background(), protocol.Message{Type: protocol.TypeOperatorReview, RequestID: "asked", ReviewOp: protocol.ReviewOpAsked, ProtocolVersion: 25})
	messageSyncEqual(t, asked.Type, protocol.TypeOperatorReviewResult)
	if asked.ReviewAsked == nil {
		t.Fatal("asked page missing")
	}
	messageSyncEqual(t, asked.ReviewAsked.Total, 2)
	epochs := map[int64]bool{}
	for _, item := range asked.ReviewAsked.Items {
		epochs[item.PairingEpoch] = true
	}
	messageSyncEqual(t, epochs[1], true)
	messageSyncEqual(t, epochs[2], true)
	failed := f.h.pendingReviews(context.Background(), protocol.Message{Type: protocol.TypeOperatorReview, RequestID: "failed", ReviewOp: protocol.ReviewOpFailed, ProtocolVersion: 25})
	messageSyncEqual(t, failed.Type, protocol.TypeOperatorReviewResult)
	if failed.ReviewFailed == nil {
		t.Fatal("failed page missing")
	}
	messageSyncEqual(t, failed.ReviewFailed.Total, 1)
	messageSyncEqual(t, len(failed.ReviewFailed.Items), 1)
	messageSyncEqual(t, failed.ReviewFailed.Items[0].PairingEpoch, int64(1))
}
