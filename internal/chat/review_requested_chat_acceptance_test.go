package chat_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

const requestedChatHead = "abcdef0123456789abcdef0123456789abcdef01"
const requestedChatBase = "123456789abcdef0123456789abcdef0123456789"

func requestedChatDecode(t *testing.T, into any) {
	t.Helper()
	data := `{"contact":"sha256:peer","ask_id":"stored-ask","reviewer":"reviewer","state":"requested","forge":"github","requested":{"base":"` + requestedChatBase + `","head":"` + requestedChatHead + `"}}`
	if err := json.Unmarshal([]byte(data), into); err != nil {
		t.Fatal(err)
	}
}

func requestedChatList(t *testing.T) (*chatHarness, protocol.ReviewAskedItem) {
	t.Helper()
	h := reviewCountsHarness(t, &protocol.ReviewCounts{AskedOpen: 1})
	var item protocol.ReviewAskedItem
	requestedChatDecode(t, &item)
	v2Command(h, "/reviews")
	for i := 0; i < 3; i++ {
		req := h.take(protocol.TypeOperatorReview)
		if req.ReviewOp == protocol.ReviewOpAsked {
			h.reply(req, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewAsked: &protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{item}, Total: 1}})
		} else {
			reviewPendingResponse(h, req, protocol.ReviewAwaitingPage{})
		}
	}
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h, item
}

func requestedChatVisible(t *testing.T, h *chatHarness, head string) {
	t.Helper()
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, head)
	if !strings.Contains(strings.ToLower(view), "pinned") && !strings.Contains(strings.ToLower(view), "requested") {
		t.Fatal("stored commit is not identified as the requested/pinned revision")
	}
	chatEqual(t, "reading pins emits no mutation", h.m.State().InFlight, 0)
}

func TestRequestedChatAcceptanceListShowsStoredHeadWithoutSessionCard(t *testing.T) {
	h, _ := requestedChatList(t)
	chatEqual(t, "fresh session has no creation-time card", len(h.m.State().Cards), 0)
	requestedChatVisible(t, h, requestedChatHead[:7])
}

func TestRequestedChatAcceptanceDetailShowsOriginalFullPins(t *testing.T) {
	h, item := requestedChatList(t)
	v2Command(h, "/reviews findings a1")
	req := h.take(protocol.TypeOperatorReview)
	chatEqual(t, "detail is a read", req.ReviewOp, protocol.ReviewOpAskedDetail)
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewAsked: &protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{item}, Total: 1}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	requestedChatVisible(t, h, requestedChatHead)
	v3Contains(t, ansi.Strip(h.m.View().Content), requestedChatBase)
}

func TestRequestedChatAcceptanceRestoredConversationShowsStoredHead(t *testing.T) {
	row := contactLayoutRow("stored-ask", 1)
	row.Direction, row.State = "out", "delivered"
	var outcome protocol.ReviewOutcomeView
	requestedChatDecode(t, &outcome)
	row.ReviewOutcome = &outcome
	h := contactLayoutHarness(t, row)
	chatEqual(t, "restored history has no creation-time card", len(h.m.State().Cards), 0)
	requestedChatVisible(t, h, requestedChatHead[:7])
}
