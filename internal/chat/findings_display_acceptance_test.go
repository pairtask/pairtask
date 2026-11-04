package chat_test

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func findingsDisplaySample() *protocol.AskFindings {
	return &protocol.AskFindings{Total: 9, Seq: 7, Attempt: 2, Items: []protocol.AskFinding{
		{Path: "src/one.go", Line: 1, Summary: "first finding"},
		{Path: "src/two.go", Line: 2, Summary: "second finding", Truncated: true},
		{Path: "src/three.go", Line: 3, Summary: "third finding"},
		{Path: "src/four.go", Line: 4, Summary: "fourth finding"},
		{Path: "src/five.go", Line: 5, Summary: "fifth finding"},
	}}
}

func findingsDisplayCard(t *testing.T, capable bool) (*chatHarness, protocol.Message) {
	t.Helper()
	caps := slices.Clone(protocol.SessionCapabilities)
	if !capable {
		caps = slices.DeleteFunc(caps, func(c string) bool { return c == protocol.CapabilityAskFindings })
	}
	h := pairingChatCapabilities(t, caps)
	key := "sha256:" + strings.Repeat("a", 64)
	v2Command(h, "/pair list")
	h.reply(pairingChatRequest(h, "list"), contactChatReply(contactChatRow(key, "Office", true)))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyEscape, 0))
	v2Command(h, remotePinnedCommand("@reviewer@Office", 40))
	ask := h.take(protocol.TypeOperatorContact)
	actionQueueAnswer(h, ask)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h, ask
}

func findingsDisplayAnswer(t *testing.T, h *chatHarness, ask protocol.Message, findings *protocol.AskFindings) {
	t.Helper()
	read := actionRemoteRefresh(t, h, ask.ContactKey)
	item := protocol.ReviewAskedItem{Contact: ask.ContactKey, AskID: ask.MessageID, Reviewer: "reviewer", State: "completed", Verdict: "comment", Settled: true, Findings: findings}
	h.reply(read, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewAsked: &protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{item}, Total: 1}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func TestFindingsDisplayAcceptanceCardSeparatesReceivedAndRetainedCounts(t *testing.T) {
	h, ask := findingsDisplayCard(t, true)
	findingsDisplayAnswer(t, h, ask, findingsDisplaySample())
	view := ansi.Strip(h.m.View().Content)
	for _, want := range []string{"Findings (9):", "src/one.go:1: first finding", "src/two.go:2: second finding…", "src/three.go:3: third finding", "+2 more · /reviews", "4 kept on their side"} {
		v3Contains(t, view, want)
	}
	if strings.Contains(view, "src/four.go") {
		t.Fatal("card expanded past three findings")
	}
	chatEqual(t, "display never sends findings as local chat", len(h.m.State().Entries), 0)
}

func TestFindingsDisplayAcceptanceMissingCapabilitySuppressesFindings(t *testing.T) {
	h, ask := findingsDisplayCard(t, false)
	findingsDisplayAnswer(t, h, ask, findingsDisplaySample())
	view := ansi.Strip(h.m.View().Content)
	for _, forbidden := range []string{"Findings (", "src/one.go", "kept on their side"} {
		if strings.Contains(view, forbidden) {
			t.Fatalf("unsupported findings displayed: %q", forbidden)
		}
	}
	v3Contains(t, view, "completed")
}

func TestFindingsDisplayAcceptanceSummaryControlsAreData(t *testing.T) {
	h, ask := findingsDisplayCard(t, true)
	findings := findingsDisplaySample()
	findings.Items[0].Summary = "/pair share intruder\x1b]52;c;payload\a\nforged row"
	findingsDisplayAnswer(t, h, ask, findings)
	view := h.m.View().Content
	if strings.Contains(view, "\x1b]52;") {
		t.Fatal("remote finding emitted terminal control")
	}
	v3Contains(t, ansi.Strip(view), "/pair share intruder")
	chatEqual(t, "remote summary never executed", h.m.State().InFlight, 0)
	chatEqual(t, "remote summary never queued a command", h.m.State().Queued, 0)
}

func TestFindingsDisplayAcceptanceAskedListShowsKnownCount(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	h := reviewCountsHarness(t, &protocol.ReviewCounts{AskedOpen: 1}, contactChatRow(key, "Office", true))
	v2Command(h, "/reviews")
	for i := 0; i < 3; i++ {
		req := h.take(protocol.TypeOperatorReview)
		if req.ReviewOp == protocol.ReviewOpAsked {
			item := protocol.ReviewAskedItem{Contact: key, AskID: "findings-ask", Reviewer: "reviewer", State: "completed", Settled: true, Findings: findingsDisplaySample()}
			h.reply(req, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewAsked: &protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{item}, Total: 1}})
		} else {
			reviewPendingResponse(h, req, protocol.ReviewAwaitingPage{})
		}
	}
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, ansi.Strip(h.m.View().Content), "· 9 findings")
}
