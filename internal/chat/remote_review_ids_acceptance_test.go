package chat_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestRemoteReviewIDsChatAcceptanceDetailNamesRequesterAskSeparately(t *testing.T) {
	h := reviewChatHarness(t)
	row := reviewChatRow("local-review-identifier")
	row.Remote = &protocol.Remote{Contact: "sha256:" + strings.Repeat("a", 64), Label: "peer", Sender: "requester"}
	row.RemoteRequest = "requester-ask-identifier"
	v2Command(h, "/review status local-review-identifier")
	req := h.take(protocol.TypeOperatorReview)
	reviewChatSettle(h, req, row)
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "requester's ask ID requester-ask-identifier")
	v3Contains(t, view, "local-review-identifier")
}

func TestRemoteReviewIDsChatAcceptanceIncomingCardNamesRequesterAndLocalReview(t *testing.T) {
	row := contactLayoutRow("requester-ask-identifier", 1)
	row.ReviewAsk = &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "259"}, Base: "base", Head: "head"}
	row.ReviewID, row.ReviewState = "local-review-identifier", "imported"
	h := contactLayoutHarness(t, row)
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "requester's ask ID requester-ask-identifier")
	v3Contains(t, view, "local-review-identifier")
}

func TestRemoteReviewIDsChatAcceptanceOutcomeKnownAndUnknownLabels(t *testing.T) {
	for _, id := range []string{"", "foreign-review-identifier"} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			row := contactLayoutRow("requester-ask", 1)
			row.Direction, row.State = "out", "delivered"
			row.ReviewOutcome = &protocol.ReviewOutcomeView{AskID: "requester-ask", Reviewer: "reviewer", State: "accepted", ReviewID: id}
			h := contactLayoutHarness(t, row)
			view := ansi.Strip(h.m.View().Content)
			want := "reviewer ID not reported"
			if id != "" {
				want = "their review ID " + id
			}
			v3Contains(t, view, want)
			if strings.Contains(view, "older pairtask") {
				t.Fatal("unknown ID was falsely diagnosed as old software")
			}
			for _, size := range []tea.WindowSizeMsg{{Width: 45, Height: 25}, {Width: 80, Height: 30}} {
				h.input(size)
				v2FrameFits(t, h, size.Width, size.Height)
			}
		})
	}
}

func TestRemoteReviewIDsChatAcceptanceAskedPanelLabelsForeignReview(t *testing.T) {
	for _, id := range []string{"", "foreign-panel-review"} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			h := reviewCountsHarness(t, &protocol.ReviewCounts{AskedOpen: 1})
			v2Command(h, "/reviews")
			for i := 0; i < 3; i++ {
				req := h.take(protocol.TypeOperatorReview)
				if req.ReviewOp == "asked" {
					h.reply(req, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewAsked: &protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{{Contact: "sha256:" + strings.Repeat("a", 64), AskID: "own-ask", Reviewer: "reviewer", ReviewID: id, State: "accepted", UpdatedAt: time.Now()}}, Total: 1}})
					continue
				}
				reviewPendingResponse(h, req, protocol.ReviewAwaitingPage{})
			}
			h.await(func() bool { return h.m.State().InFlight == 0 })
			want := "reviewer ID not reported"
			if id != "" {
				want = "their review ID " + id
			}
			v3Contains(t, ansi.Strip(h.m.View().Content), want)
		})
	}
}
