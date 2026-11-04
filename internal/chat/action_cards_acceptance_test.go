package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
	"strings"
	"testing"
	"time"
)

func actionPendingPage(key string, shared []string) protocol.Message {
	page := contactMessagesPage(key)
	page.ContactPage.Shared = shared
	page.ContactPage.SharedDigest = store.DirectoryDigest(shared)
	page.ContactPage.SharedSync = protocol.SharedSyncPending
	return page
}

func actionShareCard(t *testing.T, verb string) (*chatHarness, string) {
	t.Helper()
	h, key := sharePickerHarness(t, "sender")
	v2Type(h, "keep this draft")
	selected := h.m.State().Selected
	v2Command(h, "/pair "+verb+" sender")
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "explicit action verb", req.ContactOp, verb)
	shared := []string{"sender"}
	if verb == protocol.ContactOpUnshare {
		shared = nil
	}
	h.reply(req, actionPendingPage(key, shared))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "action leaves draft alone", h.m.State().Draft, "keep this draft")
	chatEqual(t, "action leaves selected workspace alone", h.m.State().Selected, selected)
	return h, key
}

func TestActionCardsAcceptanceShareAndUnshareShowLocalEvidence(t *testing.T) {
	for _, verb := range []string{protocol.ContactOpShare, protocol.ContactOpUnshare} {
		t.Run(verb, func(t *testing.T) {
			h, _ := actionShareCard(t, verb)
			view := ansi.Strip(h.m.View().Content)
			for _, want := range []string{"Saved here", "Peer update pending", "Office", "sender"} {
				v3Contains(t, view, want)
			}
			if strings.Contains(view, "Their side has it") {
				t.Fatal("local success claimed a peer acknowledgment")
			}
		})
	}
}

func TestActionCardsAcceptancePendingCardSurvivesBackgroundRefresh(t *testing.T) {
	h, key := actionShareCard(t, protocol.ContactOpShare)
	selected := h.m.State().Selected
	revision := h.m.State().Revision + 1
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventContactMessagesChanged, ContactKey: key, Watermark: protocol.Watermark{Epoch: "epoch", Revision: revision}}}
	req := h.take(protocol.TypeOperatorContact)
	h.reply(req, actionPendingPage(key, []string{"sender"}))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := ansi.Strip(h.m.View().Content)
	chatEqual(t, "same action is updated instead of duplicated", strings.Count(view, "Saved here"), 1)
	v3Contains(t, view, "Peer update pending")
	chatEqual(t, "background refresh leaves draft", h.m.State().Draft, "keep this draft")
	chatEqual(t, "background refresh leaves selected workspace", h.m.State().Selected, selected)
}

func TestActionCardsAcceptancePendingCardFitsWithoutColour(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	h, _ := actionShareCard(t, protocol.ContactOpShare)
	for _, size := range []tea.WindowSizeMsg{{Width: 45, Height: 24}, {Width: 80, Height: 30}} {
		h.input(size)
		v2FrameFits(t, h, size.Width, size.Height)
		v3Contains(t, ansi.Strip(h.m.View().Content), "Saved here")
	}
}

func actionRefreshCard(t *testing.T, h *chatHarness, key string, page protocol.Message) string {
	t.Helper()
	revision := h.m.State().Revision + 1
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventContactMessagesChanged, ContactKey: key, Watermark: protocol.Watermark{Epoch: "epoch", Revision: revision}}}
	req := h.take(protocol.TypeOperatorContact)
	h.reply(req, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return ansi.Strip(h.m.View().Content)
}

func TestActionCardsAcceptanceOnlyMatchingReceiptConfirms(t *testing.T) {
	h, key := actionShareCard(t, protocol.ContactOpShare)
	page := actionPendingPage(key, []string{"sender"})
	at := time.Now().Add(time.Hour)
	page.ContactPage.SharedAckAt = &at
	page.ContactPage.SharedAckDigest = store.DirectoryDigest(nil)
	view := actionRefreshCard(t, h, key, page)
	v3Contains(t, view, "Peer update pending")
	if strings.Contains(view, "Their side has it") {
		t.Fatal("an old set's receipt confirmed the new share")
	}
	page.ContactPage.SharedAckDigest = page.ContactPage.SharedDigest
	page.ContactPage.SharedSync = protocol.SharedSyncConfirmed
	view = actionRefreshCard(t, h, key, page)
	v3Contains(t, view, "Their side has it ("+at.Local().Format("15:04")+")")
	chatEqual(t, "receipt updates one card", strings.Count(view, "Saved here"), 1)
	chatEqual(t, "receipt preserves draft", h.m.State().Draft, "keep this draft")
}

func TestActionCardsAcceptanceTimestampAloneDoesNotConfirm(t *testing.T) {
	h, key := actionShareCard(t, protocol.ContactOpShare)
	page := actionPendingPage(key, []string{"sender"})
	at := time.Now().Add(time.Hour)
	page.ContactPage.SharedAckAt = &at
	// A later sync time is insufficient without a receipt for this exact set.
	view := actionRefreshCard(t, h, key, page)
	v3Contains(t, view, "Peer update pending")
}

func TestActionCardsAcceptancePreexistingReceiptSaysAlreadyHadIt(t *testing.T) {
	h, key := actionShareCard(t, protocol.ContactOpShare)
	page := actionPendingPage(key, []string{"sender"})
	at := time.Now().Add(-time.Hour)
	page.ContactPage.SharedAckAt = &at
	page.ContactPage.SharedAckDigest = page.ContactPage.SharedDigest
	page.ContactPage.SharedSync = protocol.SharedSyncConfirmed
	view := actionRefreshCard(t, h, key, page)
	v3Contains(t, view, "Their side already had it (since "+at.Local().Format("15:04")+")")
}

func TestActionCardsAcceptanceSupersessionIsSticky(t *testing.T) {
	h, key := actionShareCard(t, protocol.ContactOpShare)
	view := actionRefreshCard(t, h, key, actionPendingPage(key, nil))
	v3Contains(t, view, "Superseded by a later change")
	page := actionPendingPage(key, []string{"sender"})
	at := time.Now().Add(time.Hour)
	page.ContactPage.SharedAckAt = &at
	page.ContactPage.SharedAckDigest = page.ContactPage.SharedDigest
	page.ContactPage.SharedSync = protocol.SharedSyncConfirmed
	view = actionRefreshCard(t, h, key, page)
	v3Contains(t, view, "Superseded by a later change")
	if strings.Contains(view, "Their side has it") {
		t.Fatal("returning to the original set revived a superseded action")
	}
}

func TestActionCardsAcceptanceFailureSurvivesRefreshAndEscape(t *testing.T) {
	h, key := sharePickerHarness(t, "sender")
	v2Type(h, "keep this draft")
	v2Command(h, "/pair share sender")
	req := h.take(protocol.TypeOperatorContact)
	h.reply(req, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "grant write failed"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "Not saved")
	v3Contains(t, view, "grant write failed")
	if strings.Contains(view, "Saved here") {
		t.Fatal("failed write was marked saved")
	}
	view = actionRefreshCard(t, h, key, actionPendingPage(key, nil))
	v3Contains(t, view, "grant write failed")
	h.input(chatKey(tea.KeyEscape, 0))
	v3Contains(t, ansi.Strip(h.m.View().Content), "grant write failed")
	chatEqual(t, "failure leaves draft", h.m.State().Draft, "keep this draft")
}

func TestActionCardsAcceptanceAllActiveWindowIsBoundedAndOverflowIsHonest(t *testing.T) {
	h, key := actionShareCard(t, protocol.ContactOpShare)
	for i := 1; i < 33; i++ {
		v2Command(h, "/pair share sender")
		req := h.take(protocol.TypeOperatorContact)
		h.reply(req, actionPendingPage(key, []string{"sender"}))
		h.await(func() bool { return h.m.State().InFlight == 0 })
	}
	state := h.m.State()
	chatEqual(t, "session tracks at most 32 active cards", len(state.Cards), 32)
	seen := map[string]bool{}
	for _, card := range state.Cards {
		if card.ID == "" || seen[card.ID] {
			t.Fatal("distinct actions lost their distinct card identity")
		}
		seen[card.ID] = true
		chatEqual(t, "unacknowledged share remains active", card.Active, true)
		chatEqual(t, "unacknowledged share is not finished", card.Finished, false)
	}
	chatEqual(t, "one sent action explicitly untracked", state.CardOverflow.Untracked, 1)
	chatEqual(t, "only eight cards drawn", state.CardOverflow.InProgress, 24)
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "not shown as cards")
	v3Contains(t, view, "/pair status")
	v2Command(h, "/cards clear")
	state = h.m.State()
	chatEqual(t, "clear keeps all active cards", len(state.Cards), 32)
	chatEqual(t, "clear removes untracked aggregate", state.CardOverflow.Untracked, 0)
}

func TestActionCardsAcceptanceEvictedFailureRemainsVisibleUntilClear(t *testing.T) {
	h, key := sharePickerHarness(t, "sender")
	v2Command(h, "/pair share sender")
	req := h.take(protocol.TypeOperatorContact)
	h.reply(req, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "first share failed"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	for i := 0; i < 32; i++ {
		v2Command(h, "/pair share sender")
		req = h.take(protocol.TypeOperatorContact)
		h.reply(req, actionPendingPage(key, []string{"sender"}))
		h.await(func() bool { return h.m.State().InFlight == 0 })
	}
	state := h.m.State()
	chatEqual(t, "finished failure makes room for active card", len(state.Cards), 32)
	chatEqual(t, "evicted failure retained as aggregate", state.CardOverflow.Failures, 1)
	chatEqual(t, "no action went untracked", state.CardOverflow.Untracked, 0)
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "earlier failures")
	v3Contains(t, view, "/pair status")
	v2Command(h, "/cards clear")
	state = h.m.State()
	chatEqual(t, "explicit clear removes failure aggregate", state.CardOverflow.Failures, 0)
	chatEqual(t, "explicit clear retains active cards", len(state.Cards), 32)
}

func TestActionCardsAcceptanceClearRemovesConfirmedButKeepsActive(t *testing.T) {
	h, key := actionShareCard(t, protocol.ContactOpShare)
	page := actionPendingPage(key, []string{"sender"})
	at := time.Now().Add(time.Hour)
	page.ContactPage.SharedAckAt = &at
	page.ContactPage.SharedAckDigest = page.ContactPage.SharedDigest
	page.ContactPage.SharedSync = protocol.SharedSyncConfirmed
	view := actionRefreshCard(t, h, key, page)
	v3Contains(t, view, "Their side has it")
	v2Command(h, "/cards clear")
	view = ansi.Strip(h.m.View().Content)
	if strings.Contains(view, "Saved here") || strings.Contains(view, "Their side has it") {
		t.Fatal("explicit clear left the finished share card visible")
	}
	v2Command(h, "/pair unshare sender")
	req := h.take(protocol.TypeOperatorContact)
	h.reply(req, actionPendingPage(key, nil))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/cards clear")
	v3Contains(t, ansi.Strip(h.m.View().Content), "Peer update pending")
	chatEqual(t, "clearing cards preserves draft", h.m.State().Draft, "keep this draft")
}

func TestActionCardsAcceptanceClearOnlyAffectsInvokingConversation(t *testing.T) {
	h, key := actionShareCard(t, protocol.ContactOpShare)
	selected := h.m.State().Selected
	page := actionPendingPage(key, []string{"sender"})
	at := time.Now().Add(time.Hour)
	page.ContactPage.SharedAckAt = &at
	page.ContactPage.SharedAckDigest = page.ContactPage.SharedDigest
	page.ContactPage.SharedSync = protocol.SharedSyncConfirmed
	actionRefreshCard(t, h, key, page)
	state := h.m.State()
	chatEqual(t, "one finished card", len(state.Cards), 1)
	chatEqual(t, "card belongs to command conversation", state.Cards[0].Conversation, selected)
	h.input(chatKey(tea.KeyTab, tea.ModShift))
	chatEqual(t, "navigated to All", h.m.State().SelectedAll, true)
	v2Command(h, "/cards clear")
	chatEqual(t, "clearing All preserves contact card", len(h.m.State().Cards), 1)
	h.input(chatKey(tea.KeyTab, 0))
	v3Contains(t, ansi.Strip(h.m.View().Content), "Their side has it")
	v2Command(h, "/cards clear")
	chatEqual(t, "clear in owning conversation removes card", len(h.m.State().Cards), 0)
}
