package chat_test

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestReviewCountsChatRecoveryAcceptanceMissingAndFailedRepliesKeepStaleCounts(t *testing.T) {
	for _, response := range []protocol.Message{{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "storage unavailable"}, {Type: protocol.TypeOperatorReviewResult, ReviewOp: "counts"}} {
		t.Run(response.Type, func(t *testing.T) {
			h := reviewCountsHarness(t, &protocol.ReviewCounts{AwaitingYou: 7, AwaitingToken: "old", Running: 2})
			reviewCountsEvent(h, protocol.EventReviewsChanged, 2)
			req := reviewCountsRequest(h)
			h.reply(req, response)
			h.await(func() bool { return h.m.State().InFlight == 0 })
			view := ansi.Strip(h.m.View().Content)
			v3Contains(t, view, "7 need you")
			v3Contains(t, view, "stale")
			chatEqual(t, "failed read remains known", h.m.State().ReviewSignal.Known, true)
			chatEqual(t, "failed read marked stale", h.m.State().ReviewSignal.Stale, true)
			reviewCountsEvent(h, protocol.EventReviewsChanged, 3)
			req = reviewCountsRequest(h)
			reviewCountsReply(h, req, protocol.ReviewCounts{})
			h.await(func() bool { return h.m.State().InFlight == 0 })
			chatEqual(t, "successful authoritative zero clears stale", h.m.State().ReviewSignal.Stale, false)
			chatEqual(t, "successful authoritative zero accepted", h.m.State().ReviewSignal.AwaitingYou, 0)
		})
	}
}

func TestReviewCountsChatRecoveryAcceptanceOldReplyCannotOverwriteReconnectBaseline(t *testing.T) {
	h := reviewCountsHarness(t, &protocol.ReviewCounts{AwaitingYou: 1, AwaitingToken: "old"})
	reviewCountsEvent(h, protocol.EventReviewsChanged, 2)
	old := reviewCountsRequest(h)
	h.reconnectTo(newChatScript(), "epoch")
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 8}, ReviewCounts: &protocol.ReviewCounts{AwaitingYou: 9, AwaitingToken: "new"}}})
	h.await(func() bool { return h.m.State().Revision == 8 })
	// A response from the old connection is late data even if its request ID survives.
	h.reply(old, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewOp: "counts", ReviewCounts: &protocol.ReviewCounts{AwaitingYou: 2, AwaitingToken: "late"}})
	// The next contiguous event makes the harness pump the late reply first.
	reviewCountsEvent(h, protocol.EventReviewsChanged, 9)
	h.await(func() bool { return h.m.State().Revision == 9 })
	v3Contains(t, ansi.Strip(h.m.View().Content), "9 need you")
	chatEqual(t, "new snapshot baseline stays current", h.m.State().Revision, uint64(9))
}
