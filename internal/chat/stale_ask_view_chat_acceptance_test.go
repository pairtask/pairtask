package chat_test

import (
	"encoding/json"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func staleChatOutcome(t *testing.T) protocol.ReviewOutcomeView {
	t.Helper()
	var view protocol.ReviewOutcomeView
	data := `{"ask_id":"child-ask","reviewer":"reviewer","state":"declined","supersedes":"parent-ask","supersedes_pruned":true}`
	if err := json.Unmarshal([]byte(data), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestStaleAskViewsAcceptanceContactShowsPrunedReplacementContext(t *testing.T) {
	row := contactLayoutRow("child-ask", 1)
	row.Direction, row.State = "out", "delivered"
	outcome := staleChatOutcome(t)
	row.ReviewOutcome = &outcome
	h := contactLayoutHarness(t, row)
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "parent-ask")
	v3Contains(t, view, "pruned")
}

func TestStaleAskViewsAcceptanceAskedPanelShowsReplacementContext(t *testing.T) {
	h := reviewCountsHarness(t, &protocol.ReviewCounts{AskedOpen: 1})
	v2Command(h, "/reviews")
	for i := 0; i < 3; i++ {
		request := h.take(protocol.TypeOperatorReview)
		if request.ReviewOp == protocol.ReviewOpAsked {
			var page protocol.ReviewAskedPage
			data := `{"items":[{"contact":"sha256:peer","ask_id":"child-ask","reviewer":"reviewer","state":"declined","supersedes":"parent-ask","supersedes_pruned":true}],"total":1}`
			if err := json.Unmarshal([]byte(data), &page); err != nil {
				t.Fatal(err)
			}
			h.reply(request, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewAsked: &page})
			continue
		}
		reviewPendingResponse(h, request, protocol.ReviewAwaitingPage{})
	}
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "parent-ask")
	v3Contains(t, view, "pruned")
}
