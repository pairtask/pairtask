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

func reviewPendingItem(key, id string, cursor int64) protocol.ReviewAwaitingItem {
	return protocol.ReviewAwaitingItem{Contact: key, MessageID: id, Cursor: cursor, Sender: "remote-reviewer", Ask: &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "286"}, Base: "pinned-base", Head: "pinned-head"}, ReceivedAt: time.Now()}
}

func reviewPendingResponse(h *chatHarness, req protocol.Message, awaiting protocol.ReviewAwaitingPage) {
	h.t.Helper()
	switch req.ReviewOp {
	case "awaiting":
		h.reply(req, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewOp: "awaiting", ReviewAwaiting: &awaiting})
	case "asked":
		h.reply(req, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewOp: "asked", ReviewAsked: &protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{}}})
	case "list":
		h.reply(req, reviewChatPage(reviewChatRow("local-running"), reviewChatCompleted("terminal-review")))
	default:
		h.t.Fatalf("viewing reviews issued unexpected operation %q", req.ReviewOp)
	}
}

func reviewPendingOpen(h *chatHarness, page protocol.ReviewAwaitingPage) {
	h.t.Helper()
	v2Command(h, "/reviews")
	for i := 0; i < 3; i++ {
		reviewPendingResponse(h, h.take(protocol.TypeOperatorReview), page)
	}
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func reviewNavigationFixture(t *testing.T, digit, id string, cursor int64) (*chatHarness, string, protocol.ReviewAwaitingItem) {
	t.Helper()
	key := "sha256:" + strings.Repeat(digit, 64)
	h := reviewCountsHarness(t, &protocol.ReviewCounts{AwaitingYou: 1, AwaitingToken: "fixture"}, contactChatRow(key, "remote", true))
	item := reviewPendingItem(key, id, cursor)
	reviewPendingOpen(h, protocol.ReviewAwaitingPage{Items: []protocol.ReviewAwaitingItem{item}, Total: 1, Token: "fixture"})
	return h, key, item
}

func reviewNavigationTarget(item protocol.ReviewAwaitingItem) protocol.ContactMessageView {
	return protocol.ContactMessageView{Contact: item.Contact, ID: item.MessageID, Cursor: item.Cursor, Direction: "in", State: "routed", Text: "review request", ReviewAsk: item.Ask, ReviewState: "awaiting", CreatedAt: item.ReceivedAt}
}

func reviewNavigationSettle(h *chatHarness, req protocol.Message, rows ...protocol.ContactMessageView) {
	h.t.Helper()
	h.reply(req, contactMessagesPage(req.ContactKey, rows...))
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func TestReviewPendingNavigationAcceptanceUnloadedContactOpensExactInboundCursor(t *testing.T) {
	h, key, item := reviewNavigationFixture(t, "d", "ask-not-loaded", 431)
	view := ansi.Strip(h.m.View().Content)
	for _, label := range []string{"Needs you", "Running here", "Asked by you", "All reviews here", "terminal-rev"} {
		v3Contains(t, view, label)
	}
	v2Command(h, "/reviews open n1")
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "navigation is read only", req.ContactOp, protocol.ContactOpList)
	chatEqual(t, "unloaded canonical contact", req.ContactKey, key)
	chatEqual(t, "unloaded thread starts at newest", req.ContactBefore, int64(0))
	newest := contactMessagesPage(key, reviewNavigationRow(900), reviewNavigationRow(800))
	newest.ContactPage.More = true
	h.reply(req, newest)
	older := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "navigation loads continuous older boundary", older.ContactBefore, int64(800))
	reviewNavigationSettle(h, older, reviewNavigationTarget(item))
	v3Contains(t, ansi.Strip(h.m.View().Content), "pinned-head")
	chatEqual(t, "viewing has no local receipt side effects", len(h.m.State().QueuedAcks), 0)
	chatEqual(t, "viewing has no local transcript dispatch", len(h.m.State().Entries), 0)
}

func TestReviewPendingNavigationAcceptancePrunedRowDoesNotPretendNewestIsTarget(t *testing.T) {
	h, _, _ := reviewNavigationFixture(t, "e", "gone", 0)
	v2Command(h, "/reviews open n1")
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "pruned target opens newest", req.ContactBefore, int64(0))
	reviewNavigationSettle(h, req)
	v3Contains(t, ansi.Strip(h.m.View().Content), "history pruned")
}

func TestReviewPendingNavigationAcceptanceMoreUsesOpaqueCursorAndKeepsRefs(t *testing.T) {
	key := "sha256:" + strings.Repeat("f", 64)
	h := reviewCountsHarness(t, &protocol.ReviewCounts{AwaitingYou: 2, AwaitingToken: "AB"}, contactChatRow(key, "remote", true))
	first := reviewPendingItem(key, "first", 400)
	reviewPendingOpen(h, protocol.ReviewAwaitingPage{Items: []protocol.ReviewAwaitingItem{first}, Total: 2, More: true, Next: "opaque-cursor"})
	v2Command(h, "/reviews more needs")
	req := h.take(protocol.TypeOperatorReview)
	chatEqual(t, "page correct section", req.ReviewOp, "awaiting")
	chatEqual(t, "cursor forwarded unchanged", req.ReviewBefore, "opaque-cursor")
	second := reviewPendingItem(key, "second", 300)
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewOp: "awaiting", ReviewAwaiting: &protocol.ReviewAwaitingPage{Items: []protocol.ReviewAwaitingItem{first, second}, Total: 2}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/reviews open n2")
	open := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "unloaded appended target begins newest-first", open.ContactBefore, int64(0))
	reviewNavigationSettle(h, open, reviewNavigationTarget(second))
	v3Contains(t, h.m.State().Status, "showing n2")
}

func reviewPendingRefresh(h *chatHarness, page protocol.ReviewAwaitingPage) {
	h.t.Helper()
	reviewCountsEvent(h, protocol.EventReviewsChanged, 2)
	for i := 0; i < 4; i++ {
		req := h.take(protocol.TypeOperatorReview)
		if req.ReviewOp == "counts" {
			reviewCountsReply(h, req, protocol.ReviewCounts{AwaitingYou: page.Total, AwaitingToken: "changed"})
			continue
		}
		reviewPendingResponse(h, req, page)
	}
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func TestReviewPendingNavigationAcceptanceRefsNeverRetargetAfterRefresh(t *testing.T) {
	for _, removed := range []bool{false, true} {
		t.Run(map[bool]string{false: "reordered", true: "removed"}[removed], func(t *testing.T) {
			key := "sha256:" + strings.Repeat("a", 64)
			h := reviewCountsHarness(t, &protocol.ReviewCounts{AwaitingYou: 1, AwaitingToken: "old"}, contactChatRow(key, "remote", true))
			old, newer := reviewPendingItem(key, "old-target", 100), reviewPendingItem(key, "new-arrival", 200)
			reviewPendingOpen(h, protocol.ReviewAwaitingPage{Items: []protocol.ReviewAwaitingItem{old}, Total: 1})
			items := []protocol.ReviewAwaitingItem{newer}
			if !removed {
				items = append(items, old)
			}
			reviewPendingRefresh(h, protocol.ReviewAwaitingPage{Items: items, Total: len(items)})
			v2Command(h, "/reviews open n1")
			if removed {
				chatEqual(t, "removed identity cannot retarget", h.m.State().InFlight, 0)
				v3Contains(t, h.m.State().Status, "n1 is no longer pending")
				return
			}
			req := h.take(protocol.TypeOperatorContact)
			chatEqual(t, "unloaded stable target begins newest-first", req.ContactBefore, int64(0))
			reviewNavigationSettle(h, req, reviewNavigationRow(200), reviewNavigationTarget(old))
			v3Contains(t, h.m.State().Status, "showing n1")
		})
	}
}

func reviewNavigationRow(cursor int64) protocol.ContactMessageView {
	return protocol.ContactMessageView{ID: fmt.Sprint("history-", cursor), Cursor: cursor, Direction: "in", State: "routed", Text: fmt.Sprint("HISTORY ROW ", cursor), CreatedAt: time.Now()}
}

func TestReviewPendingNavigationAcceptanceDeepTargetContinuesFromRetainedProgress(t *testing.T) {
	h, key, item := reviewNavigationFixture(t, "b", "deep-target", 1)
	v2Command(h, "/reviews open n1")
	req := h.take(protocol.TypeOperatorContact)
	page := contactMessagesPage(key, reviewNavigationRow(1000))
	page.ContactPage.More = true
	h.reply(req, page)
	for i := 1; i <= 20; i++ {
		req = h.take(protocol.TypeOperatorContact)
		chatEqual(t, "continuous older bound", req.ContactBefore, int64(1010-i*10))
		page = contactMessagesPage(key, reviewNavigationRow(int64(1000-i*10)))
		page.ContactPage.More = true
		h.reply(req, page)
	}
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, h.m.State().Status, "/reviews open n1 continues")
	if strings.Contains(h.m.State().Status, "history pruned") {
		t.Fatal("page budget exhaustion was called history loss")
	}
	v2Command(h, "/reviews open n1")
	req = h.take(protocol.TypeOperatorContact)
	chatEqual(t, "continuation keeps oldest progress", req.ContactBefore, int64(800))
	reviewNavigationSettle(h, req, reviewNavigationTarget(item))
	v3Contains(t, h.m.State().Status, "showing n1")
	v3Contains(t, ansi.Strip(h.m.View().Content), "pinned-head")
}

func TestReviewPendingNavigationAcceptanceSwitchingTabsCancelsFurtherPaging(t *testing.T) {
	h, key, _ := reviewNavigationFixture(t, "c", "old", 1)
	v2Command(h, "/reviews open n1")
	first := h.take(protocol.TypeOperatorContact)
	page := contactMessagesPage(key, reviewNavigationRow(1000))
	page.ContactPage.More = true
	h.reply(first, page)
	older := h.take(protocol.TypeOperatorContact)
	h.input(chatKey(tea.KeyTab, tea.ModShift))
	page = contactMessagesPage(key, reviewNavigationRow(900))
	page.ContactPage.More = true
	h.reply(older, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "navigation away stays selected", h.m.State().SelectedAll, true)
	v3Contains(t, h.m.State().Status, "stopped looking for n1")
}

func TestReviewPendingNavigationAcceptanceAskedRefSelectsOutboundIdentity(t *testing.T) {
	key := "sha256:" + strings.Repeat("9", 64)
	h := reviewCountsHarness(t, &protocol.ReviewCounts{AskedOpen: 1}, contactChatRow(key, "asked", true))
	v2Command(h, "/reviews")
	for i := 0; i < 3; i++ {
		req := h.take(protocol.TypeOperatorReview)
		if req.ReviewOp == "asked" {
			h.reply(req, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewOp: "asked", ReviewAsked: &protocol.ReviewAskedPage{Items: []protocol.ReviewAskedItem{{Contact: key, AskID: "same-id", Cursor: 500, Reviewer: "reviewer", State: "asked", UpdatedAt: time.Now()}}, Total: 1}})
			continue
		}
		reviewPendingResponse(h, req, protocol.ReviewAwaitingPage{Items: []protocol.ReviewAwaitingItem{}})
	}
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/reviews open a1")
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "asked reference opens its contact", req.ContactKey, key)
	chatEqual(t, "asked unloaded navigation starts newest", req.ContactBefore, int64(0))
	in, out := reviewNavigationRow(900), reviewNavigationRow(500)
	in.ID, out.ID = "same-id", "same-id"
	out.Direction = "out"
	out.State = "delivered"
	in.Text, out.Text = "WRONG INBOUND ROW", "RIGHT OUTBOUND ROW"
	rows := []protocol.ContactMessageView{in}
	for cursor := int64(880); cursor >= 600; cursor -= 10 {
		rows = append(rows, reviewNavigationRow(cursor))
	}
	rows = append(rows, out)
	reviewNavigationSettle(h, req, rows...)
	v3Contains(t, h.m.State().Status, "showing a1")
	v3Contains(t, ansi.Strip(h.m.View().Content), "RIGHT OUTBOUND ROW")
}

func TestReviewPendingNavigationAcceptanceSameIDDoesNotScrollToOppositeDirection(t *testing.T) {
	h, _, item := reviewNavigationFixture(t, "8", "shared-id", 900)
	v2Command(h, "/reviews open n1")
	req := h.take(protocol.TypeOperatorContact)
	in, middle, out := reviewNavigationRow(900), reviewNavigationRow(600), reviewNavigationRow(500)
	in.ID, out.ID = item.MessageID, item.MessageID
	in.ReviewAsk, in.ReviewState = item.Ask, "awaiting"
	out.Direction, out.State, out.Text = "out", "delivered", "OPPOSITE DIRECTION"
	middle.Text = strings.Repeat("HISTORY SEPARATING THE TWO IDENTITIES\n", 80)
	reviewNavigationSettle(h, req, in, middle, out)
	v3Contains(t, ansi.Strip(h.m.View().Content), "pinned-head")
	v3Contains(t, h.m.State().Status, "showing n1")
}

func reviewPendingHeld(h *chatHarness, kind string) protocol.Message {
	h.t.Helper()
	if kind == "more" {
		v2Command(h, "/reviews more needs")
		return h.take(protocol.TypeOperatorReview)
	}
	v2Command(h, "/reviews")
	var held protocol.Message
	for i := 0; i < 3; i++ {
		req := h.take(protocol.TypeOperatorReview)
		if req.ReviewOp == "awaiting" {
			held = req
			continue
		}
		reviewPendingResponse(h, req, protocol.ReviewAwaitingPage{})
	}
	return held
}

func reviewPendingRace(t *testing.T) (*chatHarness, protocol.ReviewAwaitingItem) {
	t.Helper()
	key := "sha256:" + strings.Repeat("6", 64)
	h := reviewCountsHarness(t, &protocol.ReviewCounts{AwaitingYou: 1, AwaitingToken: "old"}, contactChatRow(key, "remote", true))
	old := reviewPendingItem(key, "removed", 100)
	reviewPendingOpen(h, protocol.ReviewAwaitingPage{Items: []protocol.ReviewAwaitingItem{old}, Total: 2, More: true, Next: "old-cursor"})
	return h, old
}

func reviewPendingRaceRefresh(h *chatHarness, page protocol.ReviewAwaitingPage, requests int) {
	h.t.Helper()
	reviewCountsEvent(h, protocol.EventReviewsChanged, 2)
	for i := 0; i < requests; i++ {
		req := h.take(protocol.TypeOperatorReview)
		if req.ReviewOp == "counts" {
			reviewCountsReply(h, req, protocol.ReviewCounts{AwaitingYou: 1, AwaitingToken: "new"})
			continue
		}
		reviewPendingResponse(h, req, page)
	}
	h.await(func() bool { return h.m.State().InFlight == 1 })
}

func reviewPendingAssertRemoved(h *chatHarness) {
	h.t.Helper()
	v2Command(h, "/reviews open n1")
	chatEqual(h.t, "late page cannot resurrect removed target", h.m.State().InFlight, 0)
	v3Contains(h.t, h.m.State().Status, "n1 is no longer pending")
	v2Command(h, "/reviews more needs")
	chatEqual(h.t, "late page cannot restore old pagination cursor", h.m.State().InFlight, 0)
}

func TestReviewPendingNavigationAcceptanceLateMoreCannotRestoreRemovedItemsOrCursor(t *testing.T) {
	h, old := reviewPendingRace(t)
	held := reviewPendingHeld(h, "more")
	newer := protocol.ReviewAwaitingPage{Items: []protocol.ReviewAwaitingItem{reviewPendingItem(old.Contact, "replacement", 200)}, Total: 1}
	reviewPendingRaceRefresh(h, newer, 4)
	h.reply(held, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewOp: "awaiting", ReviewAwaiting: &protocol.ReviewAwaitingPage{Items: []protocol.ReviewAwaitingItem{old}, Total: 99, More: true, Next: "stale-cursor"}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	reviewPendingAssertRemoved(h)
}

func TestReviewPendingNavigationAcceptanceDirtyFirstPageIsDiscardedBeforeQueuedRefresh(t *testing.T) {
	h, old := reviewPendingRace(t)
	held := reviewPendingHeld(h, "first")
	newer := protocol.ReviewAwaitingPage{Items: []protocol.ReviewAwaitingItem{reviewPendingItem(old.Contact, "replacement", 200)}, Total: 1}
	reviewPendingRaceRefresh(h, newer, 3)
	ghost := reviewPendingItem(old.Contact, "never-current", 300)
	h.reply(held, protocol.Message{Type: protocol.TypeOperatorReviewResult, ReviewOp: "awaiting", ReviewAwaiting: &protocol.ReviewAwaitingPage{Items: []protocol.ReviewAwaitingItem{ghost}, Total: 99, More: true, Next: "stale-cursor"}})
	fresh := h.take(protocol.TypeOperatorReview)
	chatEqual(t, "one queued first-page reread", fresh.ReviewOp, "awaiting")
	reviewPendingResponse(h, fresh, newer)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	reviewPendingAssertRemoved(h)
	v2Command(h, "/reviews open n2")
	open := h.take(protocol.TypeOperatorContact)
	row := reviewNavigationRow(200)
	row.ID = "replacement"
	reviewNavigationSettle(h, open, row)
	v3Contains(t, h.m.State().Status, "showing n2")
}
