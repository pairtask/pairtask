package chat_test

import (
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func reviewCountsHarness(t *testing.T, counts *protocol.ReviewCounts, contacts ...protocol.PairingContactView) *chatHarness {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 120, Height: 35})
	h.quiet.hold++
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 1}, ReviewCounts: counts}})
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	if len(contacts) > 0 {
		pairing := pairingChatRequest(h, "list")
		h.reply(pairing, contactChatReply(contacts...))
	}
	h.quiet.hold--
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h
}

func reviewCountsEvent(h *chatHarness, kind string, revision uint64) {
	h.t.Helper()
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: kind, Watermark: protocol.Watermark{Epoch: "epoch", Revision: revision}}}
}

func reviewCountsRequest(h *chatHarness) protocol.Message {
	h.t.Helper()
	req := h.take(protocol.TypeOperatorReview)
	chatEqual(h.t, "refresh preserves session baseline", req.ReviewOp, "counts")
	return req
}

func reviewCountsReply(h *chatHarness, req protocol.Message, counts protocol.ReviewCounts) {
	h.t.Helper()
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewOp: "counts", ReviewCounts: &counts})
}

func TestReviewCountsChatAcceptanceFooterUsesBaselineAndFitsWithoutColour(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	h := reviewCountsHarness(t, &protocol.ReviewCounts{AwaitingYou: 3, AwaitingToken: "set-A", Running: 2, AskedOpen: 4})
	view := ansi.Strip(h.m.View().Content)
	for _, want := range []string{"3 need you · /reviews", "2 running", "4 asked"} {
		v3Contains(t, view, want)
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 45, Height: 20}, {Width: 80, Height: 30}} {
		h.input(size)
		v2FrameFits(t, h, size.Width, size.Height)
	}
	chatEqual(t, "render alone sends no request", h.m.State().InFlight, 0)
}

func TestReviewCountsChatAcceptanceEventsCoalesceWithoutSnapshotRefresh(t *testing.T) {
	h := reviewCountsHarness(t, &protocol.ReviewCounts{AwaitingYou: 1, AwaitingToken: "A"})
	reviewCountsEvent(h, protocol.EventReviewsChanged, 2)
	first := reviewCountsRequest(h)
	reviewCountsEvent(h, protocol.EventContactMessagesChanged, 3)
	reviewCountsEvent(h, protocol.EventReviewsChanged, 4)
	h.await(func() bool { return h.m.State().Revision == 4 })
	chatEqual(t, "only one counts read in flight", h.m.State().InFlight, 1)
	reviewCountsReply(h, first, protocol.ReviewCounts{AwaitingYou: 2, AwaitingToken: "B"})
	second := reviewCountsRequest(h)
	reviewCountsReply(h, second, protocol.ReviewCounts{AwaitingYou: 3, AwaitingToken: "C"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, ansi.Strip(h.m.View().Content), "3 need you · /reviews")
	chatEqual(t, "counts refresh never rewinds event watermark", h.m.State().Revision, uint64(4))
}

func TestReviewCountsChatAcceptanceOlderBrokerLeavesLegacyReviewsAvailable(t *testing.T) {
	h := reviewCountsHarness(t, nil)
	older := newChatScript()
	t.Cleanup(func() { older.Close() })
	caps := slices.DeleteFunc(slices.Clone(protocol.SessionCapabilities), func(s string) bool { return s == protocol.CapabilityReviewCounts })
	h.dials <- chatDialResult{wire: older, session: protocol.SessionInfo{SessionID: "older-owner", Epoch: "epoch", Capabilities: caps}}
	h.wire.Close()
	h.wire = older
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "older broker counts remain unknown", h.m.State().ReviewSignal.Known, false)
	v2Command(h, "/reviews")
	req := h.take(protocol.TypeOperatorReview)
	chatEqual(t, "legacy list remains accessible", req.ReviewOp, protocol.ReviewOpList)
	h.reply(req, reviewChatPage(reviewChatCompleted("terminal-review")))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, ansi.Strip(h.m.View().Content), "terminal-rev")
}

func TestReviewCountsChatAcceptanceViewingKeepsBlinkAndChangedSetRemainsActive(t *testing.T) {
	h := reviewCountsHarness(t, &protocol.ReviewCounts{AwaitingYou: 2, AwaitingToken: "set-A"})
	chatEqual(t, "unseen set blinks", h.m.State().ReviewSignal.Blinking, true)
	before := h.m.State().ReviewSignal.DotShown
	h.input(chat.ReviewBlinkMsg{})
	chatEqual(t, "tick changes only dot", h.m.State().ReviewSignal.DotShown, !before)
	v3Contains(t, ansi.Strip(h.m.View().Content), "2 need you · /reviews")
	reviewPendingOpen(h, reviewBlinkPage("initial", "set-A"))
	chatEqual(t, "explicit panel view keeps process blinking", h.m.State().ReviewSignal.Blinking, true)
	chatEqual(t, "viewing does not acknowledge or decide", h.m.State().ReviewSignal.AwaitingYou, 2)
	h.input(chatKey(tea.KeyEscape, 0))
	reviewCountsEvent(h, protocol.EventReviewsChanged, 2)
	reviewCountsLoadedRefresh(h, protocol.ReviewCounts{AwaitingYou: 2, AwaitingToken: "set-B"})
	chatEqual(t, "same count with new identities keeps blinking", h.m.State().ReviewSignal.Blinking, true)
	chatEqual(t, "same count preserved", h.m.State().ReviewSignal.AwaitingYou, 2)
	reviewPendingOpen(h, reviewBlinkPage("replacement", "set-B"))
	h.input(chatKey(tea.KeyEscape, 0))
	reviewCountsEvent(h, protocol.EventReviewsChanged, 3)
	reviewCountsLoadedRefresh(h, protocol.ReviewCounts{AwaitingYou: 1, AwaitingToken: "set-C"})
	chatEqual(t, "remaining request keeps blinking after removal", h.m.State().ReviewSignal.Blinking, true)
	chatEqual(t, "removal authoritative count", h.m.State().ReviewSignal.AwaitingYou, 1)
}

func reviewBlinkPage(prefix, token string) protocol.ReviewAwaitingPage {
	key := "sha256:" + "7777777777777777777777777777777777777777777777777777777777777777"
	return protocol.ReviewAwaitingPage{Items: []protocol.ReviewAwaitingItem{reviewPendingItem(key, prefix+"-one", 1), reviewPendingItem(key, prefix+"-two", 2)}, Total: 2, Token: token}
}

func reviewCountsLoadedRefresh(h *chatHarness, counts protocol.ReviewCounts) {
	h.t.Helper()
	for i := 0; i < 2; i++ {
		req := h.take(protocol.TypeOperatorReview)
		if req.ReviewOp == protocol.ReviewOpList {
			h.reply(req, reviewChatPage())
			continue
		}
		chatEqual(h.t, "viewed panel refreshes counts too", req.ReviewOp, "counts")
		reviewCountsReply(h, req, counts)
	}
	h.await(func() bool { return h.m.State().InFlight == 0 })
}
