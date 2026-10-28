package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func reviewChatChanged(h *chatHarness, revision uint64) {
	h.t.Helper()
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventReviewsChanged, ReviewID: "review-one", Watermark: protocol.Watermark{Epoch: "epoch", Revision: revision}}}
	h.await(func() bool { return h.m.State().Revision == revision })
}

func reviewChatCompleted(id string) protocol.ReviewView {
	row := reviewChatRow(id)
	row.State = protocol.ReviewCompleted
	row.Attempt = 1
	row.Revisions = &protocol.Revisions{Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40)}
	row.Verdict = &protocol.Verdict{Outcome: protocol.OutcomeApprove, Summary: "analysis summary", Findings: []protocol.Finding{{Path: "src/example.go", Line: 9, Summary: "finding text"}}}
	row.Publication = protocol.Publication{State: protocol.PublicationPublished, Receipt: &protocol.PublicationReceipt{ReviewID: "provider-1", Event: protocol.OutcomeComment, Identity: "review-account", Revision: row.Revisions.Head, URL: "https://github.com/pairtask/pairtask/pull/201#review-1"}}
	return row
}

func TestReviewChatAcceptanceLiveRefreshCoalescesAndPreservesDetailAndDraft(t *testing.T) {
	h := reviewChatHarness(t)
	row := reviewChatCompleted("review-one")
	v2Command(h, "/reviews")
	req := h.take(protocol.TypeOperatorReview)
	h.reply(req, reviewChatPage(row))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/review status review-one")
	req = h.take(protocol.TypeOperatorReview)
	chatEqual(t, "status exact ID", req.ReviewID, row.ID)
	reviewChatSettle(h, req, row)
	h.input(tea.PasteMsg{Content: "keep this draft"})
	reviewChatChanged(h, 2)
	first := h.take(protocol.TypeOperatorReview)
	chatEqual(t, "quiet list operation", first.ReviewOp, protocol.ReviewOpList)
	for revision := uint64(3); revision <= 5; revision++ {
		reviewChatChanged(h, revision)
	}
	chatEqual(t, "one read in flight", h.m.State().InFlight, 1)
	chatEqual(t, "no redundant reads queued", h.m.State().Queued, 0)
	h.reply(first, reviewChatPage(row))
	second := h.take(protocol.TypeOperatorReview)
	row.Stale = &protocol.Revisions{Base: row.Revisions.Base, Head: strings.Repeat("c", 40)}
	h.reply(second, reviewChatPage(row))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "selected detail retained", h.m.State().ReviewSelected, row.ID)
	chatEqual(t, "panel retained", h.m.State().Panel, "reviews")
	chatEqual(t, "draft retained", h.m.State().Draft, "keep this draft")
	chatEqual(t, "no transcript entries", len(h.m.State().Entries), 0)
	view := ansi.Strip(h.m.View().Content)
	for _, want := range []string{row.Revisions.Base, row.Revisions.Head, "verdict (attempt 1): approve", "published as comment by review-account", "src/example.go:9", "finding text", "stale", row.Publication.Receipt.URL} {
		v3Contains(t, view, want)
	}
}

func TestReviewChatAcceptanceRequestedReviewUpdatesWithPanelClosed(t *testing.T) {
	h := reviewChatHarness(t)
	v2Command(h, "/review pairtask/pairtask#201 reviewer")
	req := h.take(protocol.TypeOperatorReview)
	row := reviewChatRow(req.MessageID)
	reviewChatSettle(h, req, row)
	h.input(tea.PasteMsg{Content: "untouched draft"})
	reviewChatChanged(h, 2)
	refresh := h.take(protocol.TypeOperatorReview)
	row = reviewChatCompleted(req.MessageID)
	row.Publication = protocol.Publication{State: protocol.PublicationFailed, Reason: "forbidden"}
	h.reply(refresh, reviewChatPage(row))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, h.m.State().Status, "completed")
	v3Contains(t, h.m.State().Status, "publication failed")
	chatEqual(t, "panel stays closed", h.m.State().Panel, "")
	chatEqual(t, "draft unaffected", h.m.State().Draft, "untouched draft")
	chatEqual(t, "no transcript entries", len(h.m.State().Entries), 0)
}

func TestReviewChatAcceptanceTruncatedListAndUntrustedText(t *testing.T) {
	h := reviewChatHarness(t)
	row := reviewChatCompleted("review-one")
	row.Verdict.Summary = "summary\x1b]52;c;evil\a\nforged row"
	row.Publication.Reason = "forbidden\rforged"
	v2Command(h, "/reviews")
	req := h.take(protocol.TypeOperatorReview)
	page := reviewChatPage(row)
	page.ReviewPage.Total = 4
	page.ReviewPage.Truncated = true
	h.reply(req, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, ansi.Strip(h.m.View().Content), "3 more not shown")
	v2Command(h, "/review status review-one")
	req = h.take(protocol.TypeOperatorReview)
	reviewChatSettle(h, req, row)
	view := h.m.View().Content
	chatEqual(t, "OSC command removed", strings.Contains(view, "\x1b]52"), false)
	chatEqual(t, "control bell removed", strings.Contains(view, "\a"), false)
	chatEqual(t, "carriage return removed", strings.Contains(view, "\r"), false)
	v3Contains(t, ansi.Strip(view), "publication published")
}

func TestReviewChatAcceptancePartialRefreshPreservesSelectedDetail(t *testing.T) {
	h := reviewChatHarness(t)
	row := reviewChatCompleted("review-one")
	v2Command(h, "/review status review-one")
	req := h.take(protocol.TypeOperatorReview)
	reviewChatSettle(h, req, row)
	h.input(tea.PasteMsg{Content: "saved draft"})
	reviewChatChanged(h, 2)
	refresh := h.take(protocol.TypeOperatorReview)
	page := reviewChatPage(reviewChatRow("newer-review"))
	page.ReviewPage.Total = 65
	page.ReviewPage.Truncated = true
	h.reply(refresh, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "selected ID retained", h.m.State().ReviewSelected, row.ID)
	chatEqual(t, "draft retained", h.m.State().Draft, "saved draft")
	v3Contains(t, ansi.Strip(h.m.View().Content), "analysis summary")
	v3Contains(t, ansi.Strip(h.m.View().Content), row.Revisions.Head)
}

func TestReviewChatAcceptanceReconnectRefreshesPreviouslyLoadedReviews(t *testing.T) {
	for _, requested := range []bool{false, true} {
		name := "listed"
		if requested {
			name = "requested without panel"
		}
		t.Run(name, func(t *testing.T) {
			h := reviewChatHarness(t)
			row := reviewChatRow("review-one")
			if requested {
				v2Command(h, "/review pairtask/pairtask#201 reviewer")
				req := h.take(protocol.TypeOperatorReview)
				row.ID = req.MessageID
				reviewChatSettle(h, req, row)
			} else {
				v2Command(h, "/reviews")
				req := h.take(protocol.TypeOperatorReview)
				h.reply(req, reviewChatPage(row))
				h.await(func() bool { return h.m.State().InFlight == 0 })
			}
			h.input(tea.PasteMsg{Content: "offline draft"})
			h.reconnectTo(newChatScript(), "epoch")
			inbox, history := h.baseline()
			h.reply(inbox, chatInbox())
			h.reply(history, chatHistory())
			refresh := h.take(protocol.TypeOperatorReview)
			chatEqual(t, "refresh missed events", refresh.ReviewOp, protocol.ReviewOpList)
			row = reviewChatCompleted(row.ID)
			h.reply(refresh, reviewChatPage(row))
			h.await(func() bool { return h.m.State().InFlight == 0 })
			chatEqual(t, "recovered current state", h.m.State().Reviews[0].State, protocol.ReviewCompleted)
			chatEqual(t, "draft survives", h.m.State().Draft, "offline draft")
			if requested {
				v3Contains(t, h.m.State().Status, "completed")
			}
		})
	}
}
