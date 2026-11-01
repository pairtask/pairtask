package chat_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func refusalChatRows(n int) []protocol.ContactMessageView {
	rows := contactUnreadRows(n)
	for i := range rows {
		rows[i].State, rows[i].Text = "refused", ""
		rows[i].RefusalClass, rows[i].RefusalReason = "terminal", "not_shared"
		rows[i].ReviewState, rows[i].ReviewReason = "refused", "not_shared"
		rows[i].From, rows[i].To = "remote-sender", []string{"reviewer"}
		rows[i].ReviewAsk = &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "259"}, Base: "base-pin", Head: "head-pin"}
	}
	return rows
}

func TestRefusalVisibilityChatAcceptanceExplainsFutureGrantWithoutDecision(t *testing.T) {
	rows := refusalChatRows(1)
	at := time.Now()
	rows[0].ReadAt = &at
	h, _ := contactMessagesHarness(t, rows...)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := ansi.Strip(h.m.View().Content)
	for _, want := range []string{"refused: not shared here", "/pair reviews reviewer ask", "future", "new"} {
		v3Contains(t, view, want)
	}
	for _, forbidden := range []string{"/pair approve", "decline", "text no longer kept"} {
		if strings.Contains(view, forbidden) {
			t.Fatalf("refusal offers misleading action or text: %q", forbidden)
		}
	}
	chatEqual(t, "render triggers no grant", h.m.State().InFlight, 0)
	chatEqual(t, "no local transcript delivery", len(h.m.State().Entries), 0)
}

func TestRefusalVisibilityChatAcceptanceRendersUntrustedFieldsSafely(t *testing.T) {
	rows := refusalChatRows(1)
	at := time.Now()
	rows[0].ReadAt = &at
	rows[0].From = "sender\x1b[2J"
	rows[0].To = []string{"reviewer\x1b[2J"}
	rows[0].ReviewAsk.Repository = "repo\x1b[2J"
	h, _ := contactMessagesHarness(t, rows...)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := h.m.View().Content
	if strings.Contains(view, "\x1b[2J") {
		t.Fatal("untrusted refusal fields injected terminal control")
	}
	v3Contains(t, ansi.Strip(view), "reviewer")
	chatEqual(t, "untrusted text is inert", h.m.State().InFlight, 0)
}

func TestRefusalVisibilityChatAcceptanceUnsafeRecipientUsesPlaceholder(t *testing.T) {
	rows := refusalChatRows(1)
	at := time.Now()
	rows[0].ReadAt = &at
	rows[0].To = []string{"reviewer --publish --formal"}
	h, _ := contactMessagesHarness(t, rows...)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "/pair reviews AGENT ask")
	if strings.Contains(view, "/pair reviews reviewer --publish --formal ask") {
		t.Fatal("untrusted recipient became policy flags in grant guidance")
	}
	chatEqual(t, "no implicit grant", h.m.State().InFlight, 0)
}

func TestRefusalVisibilityChatAcceptanceReadsOnlyVisibleNotices(t *testing.T) {
	h, _ := contactMessagesHarness(t, refusalChatRows(32)...)
	read := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "notice read operation", read.ContactOp, protocol.ContactOpRead)
	chatEqual(t, "some notices visible", len(read.ContactRead) > 0, true)
	chatEqual(t, "offscreen notices stay unread", len(read.ContactRead) < 32, true)
	chatEqual(t, "newest notice read", slices.Contains(read.ContactRead, "incoming-32"), true)
	chatEqual(t, "oldest notice offscreen", slices.Contains(read.ContactRead, "incoming-1"), false)
	contactReadReply(h, read)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	for i := 0; i < 3; i++ {
		h.m.View()
	}
	chatEqual(t, "view does not reread", h.m.State().InFlight, 0)
	h.input(chatKey(tea.KeyPgUp, 0))
	older := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "scroll reads newly visible notices", older.ContactOp, protocol.ContactOpRead)
	for _, id := range older.ContactRead {
		if slices.Contains(read.ContactRead, id) {
			t.Fatalf("confirmed notice read again: %s", id)
		}
	}
}

func TestRefusalVisibilityChatAcceptanceHiddenRefreshDoesNotRead(t *testing.T) {
	h, key := contactMessagesHarness(t)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyTab, tea.ModShift))
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventContactMessagesChanged, ContactKey: key, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	refresh := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "hidden refresh lists only", refresh.ContactOp, protocol.ContactOpList)
	h.reply(refresh, contactMessagesPage(key, refusalChatRows(1)...))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "hidden notice does not read", h.m.State().InFlight, 0)
	chatEqual(t, "contact remains hidden", h.m.State().SelectedAll, true)
	h.input(chatKey(tea.KeyTab, 0))
	read := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "opening contact reads notice", read.ContactOp, protocol.ContactOpRead)
	if !slices.Equal(read.ContactRead, []string{"incoming-1"}) {
		t.Fatalf("wrong visible notice set: %v", read.ContactRead)
	}
}

func TestRefusalVisibilityChatAcceptanceLatePageCannotClearRead(t *testing.T) {
	rows := refusalChatRows(1)
	at := time.Now()
	rows[0].ReadAt = &at
	h, key := contactMessagesHarness(t, rows...)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyTab, tea.ModShift))
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventContactMessagesChanged, ContactKey: key, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	refresh := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "refresh page", refresh.ContactOp, protocol.ContactOpList)
	// Same refusal state, but the page was captured before the local read.
	h.reply(refresh, contactMessagesPage(key, refusalChatRows(1)...))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyTab, 0))
	chatEqual(t, "late page preserves read without another request", h.m.State().InFlight, 0)
	v3Contains(t, ansi.Strip(h.m.View().Content), "refused: not shared here")
}
