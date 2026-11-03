package chat_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func statusActive(n int) *int { return &n }

func statusFailureItems(n int) []protocol.ReviewFailedItem {
	var items []protocol.ReviewFailedItem
	for i := 0; i < n; i++ {
		items = append(items, protocol.ReviewFailedItem{Kind: "ask", Contact: "sha256:" + strings.Repeat("a", 64), ID: fmt.Sprintf("failed-%02d", i), State: "refused", Reviewer: "reviewer", Repository: "pairtask/pairtask", Change: "298"})
	}
	return items
}

// Open all four sections but leave Failed outstanding so a test can control
// exactly which failure evidence the panel has received.
func statusOpenFailures(h *chatHarness) protocol.Message {
	h.t.Helper()
	v2Command(h, "/reviews")
	var failed protocol.Message
	for i := 0; i < 4; i++ {
		req := h.take(protocol.TypeOperatorReview)
		if req.ReviewOp == protocol.ReviewOpFailed {
			failed = req
			continue
		}
		reviewPendingResponse(h, req, protocol.ReviewAwaitingPage{})
	}
	if failed.RequestID == "" {
		h.t.Fatal("/reviews did not load its Failed section")
	}
	return failed
}

func statusReplyFailures(h *chatHarness, req protocol.Message, page protocol.ReviewFailedPage) {
	h.t.Helper()
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewOp: protocol.ReviewOpFailed, ReviewFailed: &page})
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func TestReviewStatusIndicatorsAcceptanceActiveProcessesAlwaysBlink(t *testing.T) {
	for _, tc := range []struct {
		name   string
		counts protocol.ReviewCounts
		label  string
	}{
		{"running", protocol.ReviewCounts{Running: 1}, "1 running"},
		{"asked", protocol.ReviewCounts{AskedOpen: 1}, "1 asked"},
		{"decision", protocol.ReviewCounts{AwaitingYou: 1, AwaitingToken: "pending"}, "1 need you"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := reviewCountsHarness(t, &tc.counts)
			chatEqual(t, "active process blinks", h.m.State().ReviewSignal.Blinking, true)
			before := h.m.State().ReviewSignal.DotShown
			h.input(chat.ReviewBlinkMsg{})
			chatEqual(t, "active process dot changes on tick", h.m.State().ReviewSignal.DotShown, !before)
			view := ansi.Strip(h.m.View().Content)
			v3Contains(t, view, tc.label)
			v3Contains(t, view, "/reviews")
		})
	}
}

func TestReviewStatusIndicatorsAcceptanceViewingDoesNotStopDecisionProcess(t *testing.T) {
	h := reviewCountsHarness(t, &protocol.ReviewCounts{AwaitingYou: 2, AwaitingToken: "set-A"})
	reviewPendingOpen(h, reviewBlinkPage("initial", "set-A"))
	chatEqual(t, "viewing leaves process blinking", h.m.State().ReviewSignal.Blinking, true)
	chatEqual(t, "viewing never decides a request", h.m.State().ReviewSignal.AwaitingYou, 2)
	h.input(chatKey(tea.KeyEscape, 0))
	chatEqual(t, "closing panel leaves process blinking", h.m.State().ReviewSignal.Blinking, true)
	before := h.m.State().ReviewSignal.DotShown
	h.input(chat.ReviewBlinkMsg{})
	chatEqual(t, "blink continues after viewing", h.m.State().ReviewSignal.DotShown, !before)
}

func TestReviewStatusIndicatorsAcceptanceAuthoritativeZeroEndsProcess(t *testing.T) {
	h := reviewCountsHarness(t, &protocol.ReviewCounts{Running: 1})
	reviewCountsEvent(h, protocol.EventReviewsChanged, 2)
	reviewCountsReply(h, reviewCountsRequest(h), protocol.ReviewCounts{})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "settled counts stop blink", h.m.State().ReviewSignal.Blinking, false)
	h.input(chat.ReviewBlinkMsg{})
	chatEqual(t, "late tick never restores active dot", h.m.State().ReviewSignal.DotShown, false)
}

func TestReviewStatusIndicatorsAcceptanceNoColourDistinguishesProcessesAndFailures(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, tc := range []struct {
		name   string
		counts protocol.ReviewCounts
		glyph  string
	}{
		{"decision", protocol.ReviewCounts{AwaitingYou: 1, AwaitingToken: "pending", AskedActive: statusActive(0)}, "? 1 need you"},
		{"running", protocol.ReviewCounts{Running: 1, AskedActive: statusActive(0)}, "● 1 running"},
		{"failure", protocol.ReviewCounts{Failed: 1, FailedToken: "failure", AskedActive: statusActive(0)}, "! 1 failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := reviewCountsHarness(t, &tc.counts)
			v3Contains(t, ansi.Strip(h.m.View().Content), tc.glyph)
			for _, size := range []tea.WindowSizeMsg{{Width: 45, Height: 20}, {Width: 80, Height: 30}} {
				h.input(size)
				v2FrameFits(t, h, size.Width, size.Height)
			}
		})
	}
}

func TestReviewStatusIndicatorsAcceptanceFailureNeverMasksActiveBlink(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	h := reviewCountsHarness(t, &protocol.ReviewCounts{Failed: 1, FailedToken: "failed", Running: 2, AskedActive: statusActive(0)})
	for i := 0; i < 2; i++ {
		v3Contains(t, ansi.Strip(h.m.View().Content), "! 1 failed")
		chatEqual(t, "failure leaves process blinking", h.m.State().ReviewSignal.Blinking, true)
		h.input(chat.ReviewBlinkMsg{})
	}
}

func TestReviewStatusIndicatorsAcceptanceFailureOnlyIsSteadyAndNotGreen(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	h := reviewCountsHarness(t, &protocol.ReviewCounts{Failed: 1, FailedToken: "failed", AskedOpen: 1, AskedActive: statusActive(0)})
	chatEqual(t, "open publication failure is not an active process", h.m.State().ReviewSignal.Blinking, false)
	for i := 0; i < 2; i++ {
		view := ansi.Strip(h.m.View().Content)
		v3Contains(t, view, "! 1 failed")
		if strings.Contains(view, "● 1 asked") {
			t.Fatal("failed open ask also advertised an active green process")
		}
		h.input(chat.ReviewBlinkMsg{})
	}
}

func TestReviewStatusIndicatorsAcceptanceRequesterCardsNameRemoteLifecycle(t *testing.T) {
	for _, tc := range []struct{ state, label string }{
		{"requested", "requested there"},
		{"accepted", "accepted by reviewer@Peer"},
		{"submitted", "submitted there"},
		{"publishing", "publishing there"},
		{"cancelled", "cancelled there"},
		{"declined", "declined there"},
		{"review_expired", "expired there"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			row := contactLayoutRow("own-request", 1)
			row.Direction, row.State, row.To = "out", "delivered", []string{"reviewer"}
			row.ReviewAsk = &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "298"}, Base: "base", Head: "head"}
			row.ReviewOutcome = &protocol.ReviewOutcomeView{AskID: row.ID, Reviewer: "reviewer", State: tc.state, ReviewID: "foreign-review"}
			h := contactLayoutHarness(t, row)
			v3Contains(t, ansi.Strip(h.m.View().Content), tc.label)
			chatEqual(t, "rendering status never sends a command", h.m.State().InFlight, 0)
		})
	}
}

func TestReviewStatusIndicatorsAcceptanceLegacyCountsRetainActiveBlink(t *testing.T) {
	// A nil AskedActive identifies the old counts shape, where asked_open
	// is the available authoritative measure of remote work in progress.
	h := reviewCountsHarness(t, &protocol.ReviewCounts{AskedOpen: 1})
	chatEqual(t, "legacy open asks still blink", h.m.State().ReviewSignal.Blinking, true)
	v3Contains(t, ansi.Strip(h.m.View().Content), "1 asked")
}

func statusFailureHarness(t *testing.T, n int) *chatHarness {
	t.Helper()
	return reviewCountsHarness(t, &protocol.ReviewCounts{Failed: n, FailedToken: "set-A", AskedActive: statusActive(0)})
}

func TestReviewStatusIndicatorsAcceptanceAllFailurePagesMustBeViewed(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	h := statusFailureHarness(t, 33)
	held := statusOpenFailures(h)
	chatEqual(t, "opening before evidence keeps red", h.m.State().ReviewSignal.RedShown, true)
	items := statusFailureItems(33)
	statusReplyFailures(h, held, protocol.ReviewFailedPage{Items: items[:32], Total: 33, More: true, Next: "last-page", Token: "set-A"})
	chatEqual(t, "partial failure set remains unseen", h.m.State().ReviewSignal.RedShown, true)
	v2Command(h, "/reviews more failed")
	last := h.take(protocol.TypeOperatorReview)
	chatEqual(t, "failure pagination operation", last.ReviewOp, protocol.ReviewOpFailed)
	chatEqual(t, "failure pagination preserves cursor", last.ReviewBefore, "last-page")
	statusReplyFailures(h, last, protocol.ReviewFailedPage{Items: items[32:], Total: 33, Token: "set-A"})
	chatEqual(t, "complete displayed set clears footer red", h.m.State().ReviewSignal.RedShown, false)
	chatEqual(t, "viewing never resolves failures", h.m.State().ReviewSignal.Failed, 33)
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "Failed")
	v3Contains(t, view, "!")
}

func TestReviewStatusIndicatorsAcceptanceFailedOrMissingPageNeverMarksSeen(t *testing.T) {
	for _, response := range []protocol.Message{
		{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "failure page unavailable"},
		{Type: protocol.TypeOperatorReviewResult, ReviewOp: protocol.ReviewOpFailed},
	} {
		t.Run(response.Type, func(t *testing.T) {
			h := statusFailureHarness(t, 1)
			held := statusOpenFailures(h)
			h.reply(held, response)
			h.await(func() bool { return h.m.State().InFlight == 0 })
			chatEqual(t, "unsuccessful failure read keeps red", h.m.State().ReviewSignal.RedShown, true)
		})
	}
}

func TestReviewStatusIndicatorsAcceptanceClosedPanelCannotMarkFailureSeen(t *testing.T) {
	h := statusFailureHarness(t, 1)
	held := statusOpenFailures(h)
	h.input(chatKey(tea.KeyEscape, 0))
	statusReplyFailures(h, held, protocol.ReviewFailedPage{Items: statusFailureItems(1), Total: 1, Token: "set-A"})
	chatEqual(t, "late page in closed panel keeps red", h.m.State().ReviewSignal.RedShown, true)
}

func TestReviewStatusIndicatorsAcceptanceMixedPageTokensInvalidateAccumulatedEvidence(t *testing.T) {
	h := statusFailureHarness(t, 33)
	items := statusFailureItems(33)
	statusReplyFailures(h, statusOpenFailures(h), protocol.ReviewFailedPage{Items: items[:32], Total: 33, More: true, Next: "page-2", Token: "set-A"})
	v2Command(h, "/reviews more failed")
	last := h.take(protocol.TypeOperatorReview)
	cmd := h.holdResponse(last, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewOp: protocol.ReviewOpFailed, ReviewFailed: &protocol.ReviewFailedPage{Items: items[32:], Total: 33, Token: "set-B"}})
	chatEqual(t, "mixed failure sets never clear footer", h.m.State().ReviewSignal.RedShown, true)
	h.launch(cmd)
	restart := h.take(protocol.TypeOperatorReview)
	chatEqual(t, "set change restarts failed section", restart.ReviewOp, protocol.ReviewOpFailed)
	chatEqual(t, "set change discards old cursor", restart.ReviewBefore, "")
}

func TestReviewStatusIndicatorsAcceptanceCountsRaceCannotMarkOldFailureSetSeen(t *testing.T) {
	h := statusFailureHarness(t, 1)
	reviewCountsEvent(h, protocol.EventReviewsChanged, 2)
	counts := reviewCountsRequest(h)
	held := statusOpenFailures(h)
	cmd := h.holdResponse(counts, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewOp: protocol.ReviewOpCounts, ReviewCounts: &protocol.ReviewCounts{Failed: 1, FailedToken: "set-B", AskedActive: statusActive(0)}})
	h.launch(cmd)
	// Keep commands from the late response held: this assertion is about
	// admission of obsolete evidence, independent of its recovery requests.
	h.holdResponse(held, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewOp: protocol.ReviewOpFailed, ReviewFailed: &protocol.ReviewFailedPage{Items: statusFailureItems(1), Total: 1, Token: "set-A"}})
	chatEqual(t, "old complete page cannot clear newer failure token", h.m.State().ReviewSignal.RedShown, true)
}

func TestReviewStatusIndicatorsAcceptanceMissingCapabilityIgnoresNewFailureFields(t *testing.T) {
	h := statusFailureHarness(t, 1)
	older := newChatScript()
	t.Cleanup(func() { older.Close() })
	caps := slices.DeleteFunc(slices.Clone(protocol.SessionCapabilities), func(s string) bool { return s == protocol.CapabilityReviewFailures })
	h.dials <- chatDialResult{wire: older, session: protocol.SessionInfo{SessionID: "older-owner", Epoch: "epoch", Capabilities: caps}}
	h.wire.Close()
	h.wire = older
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}, ReviewCounts: &protocol.ReviewCounts{AskedOpen: 1, AskedActive: statusActive(0), Failed: 1, FailedToken: "new-field-from-old-capabilities"}}})
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "no capability means no red", h.m.State().ReviewSignal.RedShown, false)
	chatEqual(t, "legacy counts use asked_open", h.m.State().ReviewSignal.Process, "green")
	chatEqual(t, "legacy process still blinks", h.m.State().ReviewSignal.Blinking, true)
	// Three replies prove there is no unsupported Failed operation.
	reviewPendingOpen(h, protocol.ReviewAwaitingPage{})
}

func statusLocalRowLine(t *testing.T, h *chatHarness) string {
	t.Helper()
	for _, line := range strings.Split(ansi.Strip(h.m.View().Content), "\n") {
		if strings.Contains(line, "dot-local") && strings.Contains(line, "pairtask/pairtask") {
			return strings.TrimSpace(line)
		}
	}
	t.Fatal("local review detail row not displayed")
	return ""
}

func TestReviewStatusIndicatorsAcceptanceDetailRowsUseSameProcessAndFailureMapping(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, tc := range []struct{ state, publication, glyph string }{
		{protocol.ReviewRequested, protocol.PublicationNone, "●"},
		{protocol.ReviewReady, protocol.PublicationNone, "●"},
		{protocol.ReviewInReview, protocol.PublicationNone, "●"},
		{protocol.ReviewCompleted, protocol.PublicationFailed, "!"},
		{protocol.ReviewCompleted, protocol.PublicationUncertain, "!"},
		{protocol.ReviewCompleted, protocol.PublicationNone, ""},
		{protocol.ReviewCancelled, protocol.PublicationNone, ""},
	} {
		t.Run(tc.state+"/"+tc.publication, func(t *testing.T) {
			counts := protocol.ReviewCounts{AskedActive: statusActive(0)}
			if tc.glyph == "●" {
				counts.Running = 1
			}
			h := reviewCountsHarness(t, &counts)
			row := reviewChatRow("dot-local")
			row.State, row.Publication.State = tc.state, tc.publication
			if tc.state == protocol.ReviewCompleted {
				row.Verdict = &protocol.Verdict{Outcome: "changes_requested", Summary: "normal review result"}
			}
			v2Command(h, "/review status dot-local")
			reviewChatSettle(h, h.take(protocol.TypeOperatorReview), row)
			line := statusLocalRowLine(t, h)
			if tc.glyph == "" {
				for _, glyph := range []string{"!", "?", "●"} {
					if strings.HasPrefix(line, glyph) {
						t.Fatalf("settled normal result has status dot: %s", line)
					}
				}
				return
			}
			if !strings.HasPrefix(line, tc.glyph) {
				t.Fatalf("detail row %q lacks %q status prefix", line, tc.glyph)
			}
		})
	}
}

func TestReviewStatusIndicatorsAcceptanceReconnectCannotReuseFailurePageEvidence(t *testing.T) {
	h := statusFailureHarness(t, 1)
	old := statusOpenFailures(h)
	h.reconnectTo(newChatScript(), "epoch")
	h.quiet.hold++
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 3}, ReviewCounts: &protocol.ReviewCounts{Failed: 1, FailedToken: "set-B", AskedActive: statusActive(0)}}})
	var current protocol.Message
	for i := 0; i < 6; {
		h.await(func() bool { return len(h.requests) > 0 })
		req := h.take(h.requests[0].Type)
		switch req.Type {
		case protocol.TypeOperatorReceive:
			h.reply(req, chatInbox())
		case protocol.TypeOperatorHistory:
			h.reply(req, chatHistory())
		case protocol.TypeOperatorPairing:
			h.reply(req, contactChatReply())
			continue
		case protocol.TypeOperatorReview:
			if req.ReviewOp == protocol.ReviewOpFailed {
				current = req
			} else {
				reviewPendingResponse(h, req, protocol.ReviewAwaitingPage{})
			}
		default:
			t.Fatalf("unexpected reconnect request: %+v", req)
		}
		i++
	}
	h.quiet.hold--
	if current.RequestID == "" {
		t.Fatal("reconnect omitted fresh failure section read")
	}
	h.holdResponse(old, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewOp: protocol.ReviewOpFailed, ReviewFailed: &protocol.ReviewFailedPage{Items: statusFailureItems(1), Total: 1, Token: "set-A"}})
	chatEqual(t, "old connection evidence keeps new failure unseen", h.m.State().ReviewSignal.RedShown, true)
}

func TestReviewStatusIndicatorsAcceptanceSameIdentityRefailureRearmsFooter(t *testing.T) {
	h := statusFailureHarness(t, 1)
	items := statusFailureItems(1)
	statusReplyFailures(h, statusOpenFailures(h), protocol.ReviewFailedPage{Items: items, Total: 1, Token: "set-A"})
	chatEqual(t, "original failure viewed", h.m.State().ReviewSignal.RedShown, false)
	h.input(chatKey(tea.KeyEscape, 0))
	reviewCountsEvent(h, protocol.EventReviewsChanged, 2)
	reviewCountsLoadedRefresh(h, protocol.ReviewCounts{Failed: 1, FailedToken: "set-B", AskedActive: statusActive(0)})
	chatEqual(t, "same failure count with new generation rearms red", h.m.State().ReviewSignal.RedShown, true)
	statusReplyFailures(h, statusOpenFailures(h), protocol.ReviewFailedPage{Items: items, Total: 1, Token: "set-B"})
	chatEqual(t, "new failure needs its own complete view", h.m.State().ReviewSignal.RedShown, false)
}

func statusCardLine(t *testing.T, h *chatHarness, label string) string {
	t.Helper()
	for _, line := range strings.Split(ansi.Strip(h.m.View().Content), "\n") {
		if strings.Contains(line, label) {
			return line
		}
	}
	t.Fatalf("paired review card lost status %q", label)
	return ""
}

func TestReviewStatusIndicatorsAcceptancePairedCardProcessesBlinkButRedIsSteady(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, state := range []string{"accepted", "publication_failed"} {
		t.Run(state, func(t *testing.T) {
			c := contactChatRow("sha256:"+strings.Repeat("a", 64), "Peer", true)
			counts := protocol.ReviewCounts{AskedOpen: 1, AskedActive: statusActive(1)}
			label, glyph := "accepted by reviewer@Peer", "●"
			if state == "publication_failed" {
				counts.Failed, counts.FailedToken, counts.AskedActive = 1, "failed", statusActive(0)
				label, glyph = state, "!"
			}
			h := reviewCountsHarness(t, &counts, c)
			h.input(tea.WindowSizeMsg{Width: 200, Height: 40})
			h.input(chatKey(tea.KeyTab, 0))
			req := h.take(protocol.TypeOperatorContact)
			// No ReviewAsk: retained outcomes remain useful after pruning.
			row := contactLayoutRow("retained-ask", 1)
			row.Direction, row.State, row.To = "out", "delivered", []string{"reviewer"}
			row.ReviewOutcome = &protocol.ReviewOutcomeView{AskID: row.ID, Reviewer: "reviewer", State: state}
			h.reply(req, contactMessagesPage(c.IdentityKey, row))
			h.await(func() bool { return h.m.State().InFlight == 0 })
			before := statusCardLine(t, h, label)
			chatEqual(t, "paired card starts with its status glyph", strings.Contains(before, glyph), true)
			h.input(chat.ReviewBlinkMsg{})
			after := statusCardLine(t, h, label)
			chatEqual(t, "only process card glyph blinks off", strings.Contains(after, glyph), state == "publication_failed")
		})
	}
}

func TestReviewStatusIndicatorsAcceptanceFirstFailureLoadsInAlreadyOpenPanel(t *testing.T) {
	h := reviewCountsHarness(t, &protocol.ReviewCounts{AskedActive: statusActive(0)})
	reviewPendingOpen(h, protocol.ReviewAwaitingPage{})
	reviewCountsEvent(h, protocol.EventReviewsChanged, 2)
	var counts protocol.Message
	for i := 0; i < 4; i++ {
		req := h.take(protocol.TypeOperatorReview)
		if req.ReviewOp == protocol.ReviewOpCounts {
			counts = req
		} else {
			reviewPendingResponse(h, req, protocol.ReviewAwaitingPage{})
		}
	}
	reviewCountsReply(h, counts, protocol.ReviewCounts{Failed: 1, FailedToken: "first-failure", AskedActive: statusActive(0)})
	failed := h.take(protocol.TypeOperatorReview)
	chatEqual(t, "newly nonempty failure section loads automatically", failed.ReviewOp, protocol.ReviewOpFailed)
	statusReplyFailures(h, failed, protocol.ReviewFailedPage{Items: statusFailureItems(1), Total: 1, Token: "first-failure"})
	v3Contains(t, ansi.Strip(h.m.View().Content), "Failed")
}

func TestReviewStatusIndicatorsAcceptanceStaleCountsSuppressAggregateDots(t *testing.T) {
	h := reviewCountsHarness(t, &protocol.ReviewCounts{AwaitingYou: 2, AwaitingToken: "pending", Running: 1, Failed: 1, FailedToken: "failure", AskedActive: statusActive(0)})
	reviewCountsEvent(h, protocol.EventReviewsChanged, 2)
	h.reply(reviewCountsRequest(h), protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "counts unavailable"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v := h.m.State().ReviewSignal
	chatEqual(t, "failed count read remains visibly stale", v.Stale, true)
	chatEqual(t, "stale counts retain last known values", v.AwaitingYou, 2)
	chatEqual(t, "stale aggregates do not assert a process colour", v.Process, "")
	chatEqual(t, "stale aggregates do not blink", v.Blinking, false)
	chatEqual(t, "stale aggregates do not assert failure dot", v.RedShown, false)
	v3Contains(t, ansi.Strip(h.m.View().Content), "stale")
}

func TestReviewStatusIndicatorsAcceptancePageAgainstStaleCountsCannotMarkSeen(t *testing.T) {
	h := statusFailureHarness(t, 1)
	reviewCountsEvent(h, protocol.EventReviewsChanged, 2)
	counts := reviewCountsRequest(h)
	page := statusOpenFailures(h)
	h.launch(h.holdResponse(counts, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "counts unavailable"}))
	chatEqual(t, "delayed page arrives against stale counts", h.m.State().ReviewSignal.Stale, true)
	statusReplyFailures(h, page, protocol.ReviewFailedPage{Items: statusFailureItems(1), Total: 1, Token: "set-A"})
	h.input(chatKey(tea.KeyEscape, 0))
	reviewCountsEvent(h, protocol.EventReviewsChanged, 3)
	reviewCountsLoadedRefresh(h, protocol.ReviewCounts{Failed: 1, FailedToken: "set-A", AskedActive: statusActive(0)})
	chatEqual(t, "fresh counts still require trustworthy failure evidence", h.m.State().ReviewSignal.RedShown, true)
}

func TestReviewStatusIndicatorsAcceptanceMonochromeProfileUsesDistinctGlyphs(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	for _, profile := range []colorprofile.Profile{colorprofile.ASCII, colorprofile.NoTTY} {
		t.Run(profile.String(), func(t *testing.T) {
			h := reviewCountsHarness(t, &protocol.ReviewCounts{AwaitingYou: 1, AwaitingToken: "pending", Failed: 1, FailedToken: "failure", AskedActive: statusActive(0)})
			h.input(tea.ColorProfileMsg{Profile: profile})
			view := ansi.Strip(h.m.View().Content)
			v3Contains(t, view, "! 1 failed")
			v3Contains(t, view, "? 1 need you")
		})
	}
}
