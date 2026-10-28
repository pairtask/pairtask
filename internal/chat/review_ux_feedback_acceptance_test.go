package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
	"testing"
)

func TestReviewUXRequestShowsPendingAndSelectedResult(t *testing.T) {
	h := reviewChatHarness(t)
	v2Command(h, "/review --no-publish pairtask/pairtask#201 reviewer security")
	req := h.take(protocol.TypeOperatorReview)
	chatEqual(t, "pending visible in panel", h.m.State().Panel, "reviews")
	v3Contains(t, ansi.Strip(h.m.View().Content), "requesting")
	v3Contains(t, ansi.Strip(h.m.View().Content), "reviewer")
	row := reviewChatRow(req.MessageID)
	reviewChatSettle(h, req, row)
	chatEqual(t, "result selected", h.m.State().ReviewSelected, req.MessageID)
	chatEqual(t, "result panel remains", h.m.State().Panel, "reviews")
	chatEqual(t, "no fake transcript message", len(h.m.State().Entries), 0)
	reviewChatChanged(h, 2)
	refresh := h.take(protocol.TypeOperatorReview)
	row = reviewChatCompleted(req.MessageID)
	row.Publication = protocol.Publication{State: protocol.PublicationNone}
	h.reply(refresh, reviewChatPage(row))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, ansi.Strip(h.m.View().Content), "analysis summary")
	v3Contains(t, ansi.Strip(h.m.View().Content), "finding text")
}

func TestReviewUXStatusPickerUsesStoredID(t *testing.T) {
	h := reviewChatHarness(t)
	row := reviewChatRow("5bfa4d5e-2012-4880-a3ab-4e8948134eec")
	v2Command(h, "/review status")
	if h.m.State().InFlight == 0 {
		t.Fatalf("status did not load reviews: %s", h.m.State().Status)
	}
	req := h.take(protocol.TypeOperatorReview)
	chatEqual(t, "load list", req.ReviewOp, protocol.ReviewOpList)
	h.reply(req, reviewChatPage(row))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	p := h.m.State().Picker
	if p == nil || p.Kind != "review" {
		t.Fatalf("missing review picker: %+v", p)
	}
	h.input(chatKey(tea.KeyEnter, 0))
	req = h.take(protocol.TypeOperatorReview)
	chatEqual(t, "status operation", req.ReviewOp, protocol.ReviewOpStatus)
	chatEqual(t, "exact selected ID", req.ReviewID, row.ID)
	reviewChatSettle(h, req, row)
	chatEqual(t, "selected detail", h.m.State().ReviewSelected, row.ID)
}

func TestReviewUXCancelPickerNeedsSecondEnter(t *testing.T) {
	h := reviewChatHarness(t)
	row := reviewChatRow("5bfa4d5e-2012-4880-a3ab-4e8948134eec")
	v2Command(h, "/reviews")
	req := h.take(protocol.TypeOperatorReview)
	h.reply(req, reviewChatPage(row))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/review cancel")
	if p := h.m.State().Picker; p == nil || p.Kind != "review" {
		t.Fatalf("missing cancel picker: %+v", p)
	}
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "selection does not cancel", h.m.State().InFlight, 0)
	line := h.m.State().CommandLine
	if line == nil {
		t.Fatal("cancel selection did not fill command line")
	}
	v3Contains(t, line.Text, row.ID)
	h.input(chatKey(tea.KeyEnter, 0))
	req = h.take(protocol.TypeOperatorReview)
	chatEqual(t, "explicit cancel operation", req.ReviewOp, protocol.ReviewOpCancel)
	chatEqual(t, "cancel exact ID", req.ReviewID, row.ID)
}

func TestReviewUXRefusalRemainsVisibleAndRestoresCommand(t *testing.T) {
	h := reviewChatHarness(t)
	v2Command(h, "/review --no-publish pairtask/pairtask#201 reviewer security")
	req := h.take(protocol.TypeOperatorReview)
	h.reply(req, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "review quota exceeded"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "refusal keeps panel", h.m.State().Panel, "reviews")
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "refused")
	v3Contains(t, view, "review quota exceeded")
	line := h.m.State().CommandLine
	if line == nil {
		t.Fatal("refused request command was lost")
	}
	v3Contains(t, line.Text, "pairtask/pairtask#201")
	v3Contains(t, line.Text, "--no-publish")
	chatEqual(t, "no broker transcript entry", len(h.m.State().Entries), 0)
}

func TestReviewUXReconnectKeepsVisibleRequestIdentity(t *testing.T) {
	h := reviewChatHarness(t)
	v2Command(h, "/review --no-formal pairtask/pairtask#201 reviewer")
	original := h.take(protocol.TypeOperatorReview)
	h.reconnectTo(newChatScript(), "epoch")
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	retry := h.take(protocol.TypeOperatorReview)
	chatEqual(t, "same request ID", retry.MessageID, original.MessageID)
	chatEqual(t, "pending panel retained", h.m.State().Panel, "reviews")
	row := reviewChatRow(original.MessageID)
	h.reply(retry, protocol.Message{Type: protocol.TypeOperatorReviewResult, Review: &row})
	refresh := h.take(protocol.TypeOperatorReview)
	h.reply(refresh, reviewChatPage(row))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "original result selected", h.m.State().ReviewSelected, original.MessageID)
	chatEqual(t, "one result", len(h.m.State().Reviews), 1)
	chatEqual(t, "no fake message", len(h.m.State().Entries), 0)
}
