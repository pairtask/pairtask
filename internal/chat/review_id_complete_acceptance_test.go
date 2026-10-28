package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
	"testing"
)

func reviewCompletionHarness(t *testing.T) *chatHarness {
	t.Helper()
	h := reviewChatHarness(t)
	v2Command(h, "/reviews")
	req := h.take(protocol.TypeOperatorReview)
	h.reply(req, reviewChatPage(reviewChatRow("review-one"), reviewChatRow("review-two")))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyEscape, 0))
	return h
}

func TestReviewCompletionOffersSubcommandsAndListedIDs(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{"review ", []string{"status", "cancel"}},
		{"review st", []string{"status"}},
		{"review ca", []string{"cancel"}},
		{"review status review-", []string{"review-one", "review-two"}},
		{"review cancel review-t", []string{"review-two"}},
	} {
		t.Run(tc.line, func(t *testing.T) {
			h := reviewCompletionHarness(t)
			commandCompleteLine(h, tc.line)
			commandCompleteItems(t, h, tc.want)
			chatEqual(t, "suggestions do not act", h.m.State().InFlight, 0)
		})
	}
}

func TestReviewCompletionCompletesBeforeStatusOrCancel(t *testing.T) {
	for _, op := range []string{"status", "cancel"} {
		for _, key := range []rune{tea.KeyTab, tea.KeyEnter} {
			t.Run(op+string(key), func(t *testing.T) {
				h := reviewCompletionHarness(t)
				commandCompleteLine(h, "review "+op+" review-o")
				h.input(chatKey(key, 0))
				chatEqual(t, "completed exact ID", h.m.State().CommandLine.Text, "review "+op+" review-one ")
				chatEqual(t, "completion does not execute", h.m.State().InFlight, 0)
				h.input(chatKey(tea.KeyEnter, 0))
				req := h.take(protocol.TypeOperatorReview)
				chatEqual(t, "exact ID", req.ReviewID, "review-one")
				chatEqual(t, "requested operation", string(req.ReviewOp), op)
			})
		}
	}
}

func TestReviewCompletionFullyTypedIDExecutesImmediately(t *testing.T) {
	h := reviewCompletionHarness(t)
	commandCompleteLine(h, "review status review-two")
	h.input(chatKey(tea.KeyEnter, 0))
	req := h.take(protocol.TypeOperatorReview)
	chatEqual(t, "status operation", req.ReviewOp, protocol.ReviewOpStatus)
	chatEqual(t, "exact typed ID", req.ReviewID, "review-two")
}

func TestReviewCompletionDoesNotCompleteRequestArguments(t *testing.T) {
	for _, line := range []string{"review pairtask/", "review pairtask/pairtask#208 rev", "review --no-publish pairtask/", "review status review-one extra"} {
		t.Run(line, func(t *testing.T) {
			h := reviewCompletionHarness(t)
			commandCompleteLine(h, line)
			chatEqual(t, "no argument suggestions", h.m.State().Picker, nil)
			h.input(chatKey(tea.KeyTab, 0))
			chatEqual(t, "literal text preserved", h.m.State().CommandLine.Text, line)
			chatEqual(t, "no request", h.m.State().InFlight, 0)
		})
	}
}

func TestReviewCompletionPastedTextDoesNotComplete(t *testing.T) {
	for _, tc := range []struct{ typed, pasted string }{
		{"", "review status review-o"},
		{"rev", "iew status review-o"},
		{"review status ", "review-o"},
	} {
		t.Run(tc.typed, func(t *testing.T) {
			h := reviewCompletionHarness(t)
			commandCompleteLine(h, tc.typed)
			h.input(tea.PasteMsg{Content: tc.pasted})
			h.input(chatKey(tea.KeyTab, 0))
			chatEqual(t, "paste remains literal", h.m.State().CommandLine.Text, tc.typed+tc.pasted)
			chatEqual(t, "no suggestions for paste", h.m.State().Picker, nil)
			chatEqual(t, "no request", h.m.State().InFlight, 0)
		})
	}
}
