package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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

func TestReviewCountsChatRecoveryAcceptanceZeroBaselineFailureRemainsVisiblyStale(t *testing.T) {
	h := reviewCountsHarness(t, &protocol.ReviewCounts{})
	reviewCountsEvent(h, protocol.EventReviewsChanged, 2)
	req := reviewCountsRequest(h)
	h.reply(req, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "counts unavailable"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "zero last-known count is now stale", h.m.State().ReviewSignal.Stale, true)
	v3Contains(t, ansi.Strip(h.m.View().Content), "counts stale")
}

func TestReviewCountsChatRecoveryAcceptanceFailedNeedsPageNeverMarksSetSeen(t *testing.T) {
	h := reviewCountsHarness(t, &protocol.ReviewCounts{AwaitingYou: 1, AwaitingToken: "unseen"})
	v2Command(h, "/reviews")
	for i := 0; i < 3; i++ {
		req := h.take(protocol.TypeOperatorReview)
		if req.ReviewOp == "awaiting" {
			h.reply(req, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "needs page unavailable"})
			continue
		}
		reviewPendingResponse(h, req, protocol.ReviewAwaitingPage{})
	}
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "failed needs page keeps unseen set blinking", h.m.State().ReviewSignal.Blinking, true)
	chatEqual(t, "failed view never changes authoritative count", h.m.State().ReviewSignal.AwaitingYou, 1)
}

func TestReviewCountsChatRecoveryAcceptanceOldNeedsPageCannotMarkNewerSetSeen(t *testing.T) {
	h := reviewCountsHarness(t, &protocol.ReviewCounts{AwaitingYou: 1, AwaitingToken: "set-A"})
	reviewCountsEvent(h, protocol.EventReviewsChanged, 2)
	counts := reviewCountsRequest(h)
	held := reviewPendingHeld(h, "first")
	reviewCountsReply(h, counts, protocol.ReviewCounts{AwaitingYou: 1, AwaitingToken: "set-B"})
	h.await(func() bool { return h.m.State().InFlight == 1 })
	page := protocol.ReviewAwaitingPage{Items: []protocol.ReviewAwaitingItem{reviewPendingItem("sha256:"+"7777777777777777777777777777777777777777777777777777777777777777", "old-set-member", 1)}, Total: 1, Token: "set-A"}
	reviewPendingResponse(h, held, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "old page cannot stop newer set blink", h.m.State().ReviewSignal.Blinking, true)
}

func TestReviewCountsChatRecoveryAcceptanceClosedPanelPageCannotMarkSetSeen(t *testing.T) {
	h := reviewCountsHarness(t, &protocol.ReviewCounts{AwaitingYou: 1, AwaitingToken: "unseen"})
	held := reviewPendingHeld(h, "first")
	h.input(chatKey(tea.KeyEscape, 0))
	reviewPendingResponse(h, held, protocol.ReviewAwaitingPage{Items: []protocol.ReviewAwaitingItem{reviewPendingItem("sha256:"+"7777777777777777777777777777777777777777777777777777777777777777", "not-shown", 1)}, Total: 1, Token: "unseen"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "closed panel cannot acknowledge viewing set", h.m.State().ReviewSignal.Blinking, true)
}

func reviewSectionHold(h *chatHarness, op string) protocol.Message {
	h.t.Helper()
	v2Command(h, "/reviews")
	var held protocol.Message
	for i := 0; i < 3; i++ {
		req := h.take(protocol.TypeOperatorReview)
		if req.ReviewOp == op {
			held = req
			continue
		}
		reviewPendingResponse(h, req, protocol.ReviewAwaitingPage{})
	}
	return held
}

func reviewSectionReconnect(h *chatHarness, key string) {
	h.t.Helper()
	h.reconnectTo(newChatScript(), "epoch")
	reviewSectionBaseline(h, key)
}

func reviewSectionBaseline(h *chatHarness, key string) {
	h.t.Helper()
	h.quiet.hold++
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 10}, ReviewCounts: &protocol.ReviewCounts{AwaitingYou: 2, AwaitingToken: "reconnected"}}})
	inbox := h.take(protocol.TypeOperatorReceive)
	h.reply(inbox, chatInbox())
	history := h.take(protocol.TypeOperatorHistory)
	h.reply(history, chatHistory())
	pairing := pairingChatRequest(h, "list")
	h.reply(pairing, contactChatReply(contactChatRow(key, "remote", true)))
	h.quiet.hold--
}

func TestReviewCountsChatRecoveryAcceptanceGapBaselineFencesOldSectionPage(t *testing.T) {
	key := "sha256:" + strings.Repeat("4", 64)
	h := reviewCountsHarness(t, &protocol.ReviewCounts{AwaitingYou: 1, AwaitingToken: "old"}, contactChatRow(key, "remote", true))
	old := reviewPendingItem(key, "stable", 100)
	reviewPendingOpen(h, protocol.ReviewAwaitingPage{Items: []protocol.ReviewAwaitingItem{old}, Total: 1, Token: "old"})
	held := reviewSectionHold(h, "awaiting")
	reviewCountsEvent(h, protocol.EventReviewsChanged, 10)
	page := protocol.ReviewAwaitingPage{Items: []protocol.ReviewAwaitingItem{old, reviewPendingItem(key, "fresh", 200)}, Total: 2, Token: "reconnected"}
	reviewSectionGapBaseline(h, key, page)
	h.await(func() bool { return h.m.State().InFlight == 1 })
	reviewSectionLatePage(h, held, key)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/reviews open n1")
	open := h.take(protocol.TypeOperatorContact)
	row := reviewNavigationRow(100)
	row.ID = old.MessageID
	h.reply(open, contactMessagesPage(key, row))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, h.m.State().Status, "showing n1")
}

func reviewSectionGapBaseline(h *chatHarness, key string, page protocol.ReviewAwaitingPage) {
	h.t.Helper()
	h.quiet.hold++
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 10}, ReviewCounts: &protocol.ReviewCounts{AwaitingYou: 2, AwaitingToken: "reconnected"}}})
	// Reply in arrival order: the old request occupies one of four slots.
	for i := 0; i < 6; i++ {
		h.await(func() bool { return len(h.requests) > 0 })
		req := h.take(h.requests[0].Type)
		switch req.Type {
		case protocol.TypeOperatorReceive:
			h.reply(req, chatInbox())
		case protocol.TypeOperatorHistory:
			h.reply(req, chatHistory())
		case protocol.TypeOperatorPairing:
			h.reply(req, contactChatReply(contactChatRow(key, "remote", true)))
		case protocol.TypeOperatorReview:
			reviewPendingResponse(h, req, page)
		default:
			h.t.Fatalf("unexpected baseline request %q", req.Type)
		}
	}
	h.quiet.hold--
}

func reviewSectionLatePage(h *chatHarness, held protocol.Message, key string) {
	h.t.Helper()
	late := protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewOp: held.ReviewOp}
	if held.ReviewOp == "awaiting" {
		late.ReviewAwaiting = &protocol.ReviewAwaitingPage{Items: []protocol.ReviewAwaitingItem{reviewPendingItem(key, "stale-ghost", 300)}, Total: 99, Token: "old"}
	} else {
		late.ReviewAsked = &protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{{Contact: key, AskID: "stale-ghost", Cursor: 300}}, Total: 99}
	}
	h.launch(h.holdResponse(held, late))
}

func TestReviewCountsChatRecoveryAcceptancePendingFirstPagesRecoverAcrossReconnect(t *testing.T) {
	for _, tc := range []struct {
		op     string
		reopen bool
	}{{"awaiting", false}, {"asked", false}, {"awaiting", true}, {"asked", true}} {
		t.Run(tc.op+map[bool]string{false: "-open", true: "-reopen"}[tc.reopen], func(t *testing.T) {
			key := "sha256:" + strings.Repeat("5", 64)
			h := reviewCountsHarness(t, &protocol.ReviewCounts{AwaitingYou: 1, AwaitingToken: "old"}, contactChatRow(key, "remote", true))
			old := reviewPendingItem(key, "stable", 100)
			reviewPendingOpen(h, protocol.ReviewAwaitingPage{Items: []protocol.ReviewAwaitingItem{old}, Total: 1, Token: "old"})
			held := reviewSectionHold(h, tc.op)
			if tc.reopen {
				h.input(chatKey(tea.KeyEscape, 0))
			}
			reviewSectionReconnect(h, key)
			if tc.reopen {
				quiet := h.take(protocol.TypeOperatorReview)
				chatEqual(t, "closed panel only refreshes local history", quiet.ReviewOp, "list")
				h.reply(quiet, reviewChatPage())
				h.await(func() bool { return h.m.State().InFlight == 0 })
				v2Command(h, "/reviews")
			}
			page := protocol.ReviewAwaitingPage{Items: []protocol.ReviewAwaitingItem{old, reviewPendingItem(key, "fresh", 200)}, Total: 2, Token: "reconnected"}
			for i := 0; i < 3; i++ {
				reviewPendingResponse(h, h.take(protocol.TypeOperatorReview), page)
			}
			h.await(func() bool { return h.m.State().InFlight == 0 })
			reviewSectionLatePage(h, held, key)
			v2Command(h, "/reviews open a1")
			chatEqual(t, "old asked page cannot restore a ghost", h.m.State().InFlight, 0)
			v2Command(h, "/reviews open n1")
			open := h.take(protocol.TypeOperatorContact)
			row := reviewNavigationRow(100)
			row.ID = old.MessageID
			h.reply(open, contactMessagesPage(key, row))
			h.await(func() bool { return h.m.State().InFlight == 0 })
			v3Contains(t, h.m.State().Status, "showing n1")
		})
	}
}
