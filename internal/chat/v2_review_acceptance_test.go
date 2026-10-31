package chat_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestChatV2ReviewRejectedDirectSendCannotBecomeBroadcastDraft(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	v2Type(h, "@alice private text")
	req := v2Send(h)
	h.reply(req, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "rejected"})
	h.await(func() bool { return h.m.State().Intent.State == chat.IntentRejected })
	chatEqual(t, "rejection updates intent only", h.m.State().Draft, "")
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "empty Enter cannot create new intent", h.m.State().Intent.MessageID, req.MessageID)
}

func TestChatV2ReviewEscapeDoesNotBroadenEmptyReply(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	v2History(h, chatIncoming("target", 7))
	v2Command(h, "/reply #7")
	h.input(chatKey(tea.KeyEscape, 0))
	chatEqual(t, "Escape cancels reply", h.m.State().InReplyTo, "")
	chatEqual(t, "Escape sends nothing", h.m.State().InFlight, 0)
	v2Type(h, "private reply")
	req := v2Send(h)
	chatEqual(t, "reply audience preserved", req.To, "author")
	chatEqual(t, "ordinary direct message after cancellation", req.InReplyTo, "")
}

func TestChatV2ReviewHistoryRestoresClearedRows(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	v2History(h, chatIncoming("hidden", 7))
	v2Command(h, "/clear")
	if strings.Contains(ansi.Strip(h.m.View().Content), "body hidden") {
		t.Fatal("clear did not hide transcript")
	}
	v2Command(h, "/history")
	chatEqual(t, "history resets view cutoff", h.m.State().Cutoff, uint64(0))
	if !strings.Contains(ansi.Strip(h.m.View().Content), "body hidden") {
		t.Fatal("history did not restore already loaded rows")
	}
}

func TestChatV2ReviewHelpPanelCanScrollToLastLine(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	h.input(tea.WindowSizeMsg{Width: 60, Height: 10})
	v2Command(h, "/help")
	var seen strings.Builder
	for range 30 {
		v2FrameFits(t, h, 60, 10)
		seen.WriteString(ansi.Strip(h.m.View().Content))
		h.input(chatKey(tea.KeyPgDown, 0))
	}
	if !strings.Contains(seen.String(), "/quit") {
		t.Fatal("help commands below the panel viewport are unreachable")
	}
}

func TestChatV2ReviewNarrowHeaderKeepsCommandTargetVisible(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	msg := chatIncoming("target", 12345)
	msg.From = strings.Repeat("long-agent-", 6)
	v2History(h, msg)
	h.input(tea.WindowSizeMsg{Width: 32, Height: 20})
	var seen strings.Builder
	for range 20 {
		v2FrameFits(t, h, 32, 20)
		seen.WriteString(ansi.Strip(h.m.View().Content))
		h.input(chatKey(tea.KeyPgUp, 0))
	}
	if !strings.Contains(seen.String(), "#12345") {
		t.Fatal("message sequence is clipped, leaving no visible command target")
	}
}

func TestChatV2ReviewLateOldHistoryCannotValidateEpochTarget(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	v2History(h, chatIncoming("old-target", 7))
	h.event(protocol.EventConversationChanged, "epoch", 2)
	oldRequest := h.take(protocol.TypeOperatorHistory)
	h.event(protocol.EventResyncRequired, "new-epoch", 1)
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{
		Watermark: protocol.Watermark{Epoch: "new-epoch", Revision: 1},
	}})
	h.await(func() bool { return h.m.State().Baseline && h.m.State().Epoch == "new-epoch" })
	late := chatHistory(chatIncoming("old-target", 7))
	late.History.Watermark.Revision = 2
	h.reply(oldRequest, late)
	freshRequest := h.take(protocol.TypeOperatorHistory) // Stale scan completed; fresh catch-up begins.
	v2Command(h, "/reply #7")
	chatEqual(t, "old page cannot bless target in new epoch", h.m.State().InReplyTo, "")
	h.input(chatKey(tea.KeyEscape, 0))
	v2Command(h, "/ack #7")
	chatEqual(t, "old page cannot permit new ack", len(h.m.State().QueuedAcks), 0)
	h.input(chatKey(tea.KeyEscape, 0))
	current := chatHistory(chatIncoming("current-target", 7))
	current.History.Watermark.Epoch = "new-epoch"
	h.reply(freshRequest, current)
	h.await(func() bool { return len(h.m.State().Entries) == 2 })
	v2Command(h, "/reply #7")
	chatEqual(t, "current epoch wins reused sequence", h.m.State().InReplyTo, "current-target")
}

func TestChatV2ReviewNullIsNotLiteralAudienceArray(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	v2Type(h, "draft")
	v2Command(h, "/to null")
	chatEqual(t, "JSON null is not an audience array", h.m.State().Audience.Source, "everyone")
}

func TestChatV2ReviewFindIncludesVisibleTranscript(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	msg := chatIncoming("needle", 7)
	msg.Payload, _ = json.Marshal(map[string]string{"text": "unique needle text"})
	v2History(h, msg)
	v2Command(h, "/find needle")
	if !strings.Contains(h.m.State().Status, "found #7") {
		t.Fatalf("search missed the only matching message: %q", h.m.State().Status)
	}
}

func TestChatV2ReviewResizeKeepsReadingAnchorWithinLongMessage(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	msg := chatIncoming("long", 7)
	var body strings.Builder
	for i := range 2000 {
		fmt.Fprintf(&body, "token%03d ", i)
	}
	msg.Payload, _ = json.Marshal(map[string]string{"text": body.String()})
	v2History(h, msg)
	h.input(tea.WindowSizeMsg{Width: 80, Height: 12})
	h.input(chatKey(tea.KeyPgUp, 0))
	before := ansi.Strip(h.m.View().Content)
	anchor := regexp.MustCompile(`token\d+`).FindString(before)
	if anchor == "" {
		t.Fatalf("no reading anchor in frame: %q", before)
	}
	h.input(tea.WindowSizeMsg{Width: 40, Height: 12})
	after := ansi.Strip(h.m.View().Content)
	if !strings.Contains(after, anchor) {
		t.Fatalf("resize lost reading position %s: before %q after %q", anchor, before, after)
	}
}

func TestChatV2ReviewTabInRecipientIDIsVisibleAsControl(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	v2Command(h, `/to ["a\tb"]`)
	hint := h.m.State().Hint
	if !strings.Contains(hint, `a\tb`) && !strings.Contains(hint, `a\x09b`) {
		t.Fatalf("tab-bearing ID indistinguishable from spaces: %q", hint)
	}
}

func TestChatV2ReviewUnicodeWhitespaceDoesNotCrashComposer(t *testing.T) {
	for _, text := range []string{"\u00a0", "\u2003", "\u3000"} {
		t.Run(text, func(t *testing.T) {
			h := newChatHarness(t, chat.Bounds{})
			h.ready()
			v2Type(h, text)
			h.input(chatKey(tea.KeyEnter, 0))
			chatEqual(t, "Unicode whitespace cannot send", h.m.State().Intent, nil)
			chatEqual(t, "Unicode whitespace draft retained", h.m.State().Draft, text)
		})
	}
}
