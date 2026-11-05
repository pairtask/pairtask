package chat_test

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func refusalGuidancePanel(t *testing.T, reason string) *chatHarness {
	t.Helper()
	return refusalGuidancePanelMode(t, reason, false)
}

func refusalGuidancePanelMode(t *testing.T, reason string, localFailure bool) *chatHarness {
	t.Helper()
	h := reviewCountsHarness(t, &protocol.ReviewCounts{Failed: 1, FailedToken: "guidance-set"})
	h.input(tea.WindowSizeMsg{Width: 180, Height: 50})
	v2Command(h, "/reviews")
	contact := "sha256:" + strings.Repeat("a", 64)
	for range 4 {
		request := h.take(protocol.TypeOperatorReview)
		switch request.ReviewOp {
		case protocol.ReviewOpAsked:
			item := protocol.ReviewAskedItem{Contact: contact, AskID: "refused-ask", Cursor: 13, Reviewer: "reviewer", State: "refused", Reason: reason, Settled: true, UpdatedAt: time.Now()}
			h.reply(request, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewAsked: &protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{item}, Total: 1}})
		case protocol.ReviewOpFailed:
			item := protocol.ReviewFailedItem{Kind: protocol.FailedAsk, Contact: contact, ID: "refused-ask", Cursor: 13, Reviewer: "reviewer", State: "refused", Reason: reason, Repository: "pairtask/pairtask", Change: "302", UpdatedAt: time.Now()}
			if localFailure {
				item.Kind, item.Contact, item.State = protocol.FailedReview, "", protocol.PublicationFailed
			}
			h.reply(request, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewFailed: &protocol.ReviewFailedPage{Items: []protocol.ReviewFailedItem{item}, Total: 1, Token: "guidance-set"}})
		default:
			reviewPendingResponse(h, request, protocol.ReviewAwaitingPage{})
		}
	}
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h
}

func refusalGuidanceCardFix(t *testing.T) string {
	t.Helper()
	h, ask := actionRemoteQueued(t)
	request := actionRemoteRefresh(t, h, ask.ContactKey)
	item := protocol.ReviewAskedItem{Contact: ask.ContactKey, AskID: ask.MessageID, Reviewer: "reviewer", State: "refused", Reason: "not_shared", Settled: true, UpdatedAt: time.Now()}
	h.reply(request, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewAsked: &protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{item}, Total: 1}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	for _, step := range actionCardWithID(t, h, ask.MessageID).Steps {
		if strings.Contains(step, "/pair reviews reviewer ask;") {
			return ansi.Strip(step)
		}
	}
	t.Fatal("reference action card has no not_shared remedy")
	return ""
}

func refusalGuidanceSection(t *testing.T, view, heading, next string) string {
	t.Helper()
	start := strings.Index(view, heading)
	if start < 0 {
		t.Fatalf("missing section %q", heading)
	}
	end := strings.Index(view[start+len(heading):], next)
	if end < 0 {
		t.Fatalf("missing next section %q", next)
	}
	return view[start : start+len(heading)+end]
}

func TestRefusalGuidanceAcceptanceKnownFixMatchesCardInAskedAndFailed(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	fix := refusalGuidanceCardFix(t)
	v3Contains(t, fix, "Their operator can allow it with /pair reviews reviewer ask; then send a new request.")
	h := refusalGuidancePanel(t, "not_shared")
	view := ansi.Strip(h.m.View().Content)
	for _, section := range []struct{ heading, next string }{{"Failed", "Needs you"}, {"Asked by you", "All reviews here"}} {
		t.Run(section.heading, func(t *testing.T) {
			text := refusalGuidanceSection(t, view, section.heading, section.next)
			v3Contains(t, text, fix)
		})
	}
	chatEqual(t, "guidance display never sends a new request", h.m.State().InFlight, 0)
	chatEqual(t, "guidance display never queues approvals", len(h.m.State().QueuedAcks), 0)
}

func TestRefusalGuidanceAcceptanceUnknownReasonsNeverOfferPermissionFix(t *testing.T) {
	for _, reason := range []string{"invalid", "unsupported", "unknown_refusal"} {
		t.Run(reason, func(t *testing.T) {
			h := refusalGuidancePanel(t, reason)
			view := ansi.Strip(h.m.View().Content)
			v3Contains(t, refusalGuidanceSection(t, view, "Failed", "Needs you"), reason)
			if strings.Contains(view, "/pair reviews") {
				t.Fatal("unrelated refusal invented a permission remedy")
			}
			if strings.Contains(view, "then send a new request") {
				t.Fatal("unrelated refusal invented a retry instruction")
			}
			chatEqual(t, "unknown refusal display never writes", h.m.State().InFlight, 0)
		})
	}
}

func TestRefusalGuidanceAcceptanceLocalPublicationFailureNeverOffersPeerGrant(t *testing.T) {
	h := refusalGuidancePanelMode(t, "not_shared", true)
	section := refusalGuidanceSection(t, ansi.Strip(h.m.View().Content), "Failed", "Needs you")
	v3Contains(t, section, "not_shared")
	if strings.Contains(section, "/pair reviews") {
		t.Fatal("local publication failure offered a remote permission grant")
	}
	chatEqual(t, "local failure guidance is read-only", h.m.State().InFlight, 0)
}
