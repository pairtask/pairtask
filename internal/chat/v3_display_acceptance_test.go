package chat_test

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func v3DisplayHarness(t *testing.T, partial bool) *chatHarness {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 120, Height: 24})
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	agents := []protocol.AgentPresence{{AgentID: "claude-1", SessionID: "claude", ProtocolVersion: protocol.Version}, {AgentID: "codex-1", SessionID: "codex", ProtocolVersion: protocol.Version}}
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{
		Watermark: protocol.Watermark{Epoch: "epoch", Revision: 1}, Agents: agents, AgentsTotal: 2, AgentsTruncated: partial,
	}})
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	h.reply(inbox, chatInbox())
	msg := chatIncoming("target", 7)
	msg.From = "codex-1"
	h.reply(history, chatHistory(msg))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h
}

func isAudienceRow(row string) bool {
	return strings.HasPrefix(row, "── ") || strings.HasPrefix(row, "╭─ ")
}

func v3AudienceRow(t *testing.T, h *chatHarness) string {
	t.Helper()
	for _, row := range strings.Split(h.m.View().Content, "\n") {
		if isAudienceRow(ansi.Strip(row)) {
			return row
		}
	}
	t.Fatalf("audience divider missing: %q", h.m.View().Content)
	return ""
}

func v3Contains(t *testing.T, text, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Fatalf("missing %q in %q", want, text)
	}
}

func TestChatV3DisplayHeaderAndDividersFrameTranscriptAndDraft(t *testing.T) {
	h := v3DisplayHarness(t, false)
	v2Type(h, "draft remains here")
	rows := strings.Split(ansi.Strip(h.m.View().Content), "\n")
	v3Contains(t, rows[0], "pairtask operator chat")
	v3Contains(t, rows[len(rows)-1], "connected")
	v3Contains(t, rows[len(rows)-1], "2 online")
	v3Contains(t, rows[len(rows)-1], "Enter send · / commands · F1 help")
	v3Contains(t, rows[1], "1 All")
	v3Contains(t, rows[1], "claude-1")
	v3Contains(t, rows[1], "codex-1")
	chatEqual(t, "conversation bar divider", rows[2], strings.Repeat("─", 120))
	v3Contains(t, strings.Join(rows[3:len(rows)-4], "\n"), "body target")
	v3Contains(t, rows[len(rows)-4], "╭─ to everyone (2 online) ─")
	v3Contains(t, rows[len(rows)-3], "draft remains here")
	chatEqual(t, "draft untouched by rendering", h.m.State().Draft, "draft remains here")
	chatEqual(t, "rendering makes no requests", h.m.State().InFlight, 0)
	v2FrameFits(t, h, 120, 24)
}

func TestChatV3DisplayAudienceLabelsPreserveScope(t *testing.T) {
	for _, tc := range []struct{ name, draft, command, want string }{
		{name: "everyone", want: "to everyone (2 online)"},
		{name: "direct", draft: "body", command: `/to ["codex-1"]`, want: "to codex-1"},
		{name: "group", draft: "body", command: `/to ["codex-1","claude-1"]`, want: "to claude-1, codex-1"},
		{name: "unknown", draft: "body", command: `/to ["unseen"]`, want: "to unseen (not seen: unseen)"},
		{name: "literal", command: `/to ["agent with spaces"]`, want: "to agent with spaces (not seen: agent with spaces)"},
		{name: "reply", command: "/reply #7", want: "reply to #7 → codex-1 · Esc cancels"},
		{name: "invalid", draft: "@all @codex-1 body", want: "✗ @all can't be combined with other mentions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := v3DisplayHarness(t, false)
			v2Type(h, tc.draft)
			if tc.command != "" {
				v2Command(h, tc.command)
			}
			chatEqual(t, "audience hint", h.m.State().Hint, tc.want)
			row := ansi.Strip(v3AudienceRow(t, h))
			v3Contains(t, row, tc.want)
			chatEqual(t, "divider fills terminal width", ansi.StringWidth(row), 120)
			chatEqual(t, "display does not send", h.m.State().Intent, nil)
			chatEqual(t, "display does not acknowledge", len(h.m.State().QueuedAcks), 0)
		})
	}
}

func TestChatV3DisplayKeepsPartialPresenceAndPasteWarnings(t *testing.T) {
	h := v3DisplayHarness(t, true)
	v3Contains(t, ansi.Strip(v3AudienceRow(t, h)), "to everyone (2 online, partial list)")
	h.input(tea.PasteMsg{Content: "@codex-1 literal text"})
	v3Contains(t, ansi.Strip(v3AudienceRow(t, h)), "pasted text, mentions ignored")
	req := v2Send(h)
	chatEqual(t, "pasted mention remains broadcast", req.To, protocol.Broadcast)
	chatEqual(t, "paste preserved", v2Body(t, req), "@codex-1 literal text")
}

func TestChatV3DisplayCommandAndHelpPreserveDraft(t *testing.T) {
	h := v3DisplayHarness(t, false)
	v2Type(h, "kept draft")
	h.input(chatKey('k', tea.ModCtrl))
	v3Contains(t, ansi.Strip(v3AudienceRow(t, h)), "command · Enter runs · Tab completes · Esc back to your draft")
	h.input(chatKey(tea.KeyEscape, 0))
	h.input(chatKey(tea.KeyF1, 0))
	chatEqual(t, "header and help use new title", strings.Count(ansi.Strip(h.m.View().Content), "pairtask operator chat"), 2)
	v3Contains(t, ansi.Strip(v3AudienceRow(t, h)), "to everyone")
	h.input(chatKey(tea.KeyEscape, 0))
	chatEqual(t, "draft survives command/help", h.m.State().Draft, "kept draft")
}

func TestChatV3DisplaySmallFramesKeepAudienceAndComposer(t *testing.T) {
	h := v3DisplayHarness(t, false)
	v2Type(h, "draft")
	for _, size := range [][2]int{{20, 4}, {32, 5}, {52, 8}, {76, 10}, {120, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			h.input(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			v2FrameFits(t, h, size[0], size[1])
			v3Contains(t, ansi.Strip(v3AudienceRow(t, h)), "to everyone")
			v3Contains(t, strings.Join(growthComposerRows(t, h), "\n"), "draft")
		})
	}
}

func TestChatV3DisplayDropsHeaderRuleBeforeLastTranscriptRow(t *testing.T) {
	h := v3DisplayHarness(t, false)
	v2Command(h, "/ack #999")
	h.input(chatKey(tea.KeyEscape, 0))
	if h.m.State().Status == "" {
		t.Fatal("test requires a visible status message")
	}
	h.input(tea.WindowSizeMsg{Width: 80, Height: 5})
	frame := ansi.Strip(h.m.View().Content)
	if strings.Contains(frame, strings.Repeat("─", 80)) {
		t.Fatal("decorative header divider consumed the last transcript row")
	}
	v3Contains(t, frame, "needs ack")
	v3Contains(t, frame, "to everyone")
	v2FrameFits(t, h, 80, 5)
}
