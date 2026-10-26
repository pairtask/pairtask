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

func TestChatDiagramLabelsShowRealMessageAndBlockNumbers(t *testing.T) {
	text := "```mermaid\nA --> B\n```\n```plantuml\nAlice -> Bob\n```"
	h := formattingHarness(t, text, 80, 30)
	frame := ansi.Strip(h.m.View().Content)
	for _, label := range []string{"◇ mermaid · /diagram #7 1", "◇ plantuml · /diagram #7 2"} {
		if !strings.Contains(frame, label) {
			t.Errorf("label %q missing from %q", label, frame)
		}
	}
	h = formattingHarness(t, "```mermaid\nA\n```", 80, 30)
	row := strings.TrimSpace(ansi.Strip(formattingRow(t, h, "◇ mermaid")))
	chatEqual(t, "single diagram omits block index", row, "◇ mermaid · /diagram #7")
}

func TestChatDiagramCopyAndFindKeepOriginalSource(t *testing.T) {
	text := "before\n```mermaid\n  A[**literal**] --> B\n```\nafter"
	h := formattingHarness(t, text, 80, 30)
	h.input(chatKey('k', tea.ModCtrl))
	v2Type(h, "copy #7")
	cmd := h.update(chatKey(tea.KeyEnter, 0))
	if cmd == nil {
		t.Fatal("copy command missing")
	}
	chatEqual(t, "copy raw source including fences", cmd(), tea.SetClipboard(text)())
	for _, query := range []string{"A[**literal**]", "mermaid", "after"} {
		v2Command(h, "/find "+query)
		if !strings.Contains(h.m.State().Status, "found #7") {
			t.Errorf("source search failed: %s: %s", query, h.m.State().Status)
		}
	}
}

func TestChatDiagramResizePreservesSourceAnchor(t *testing.T) {
	var source strings.Builder
	source.WriteString("```mermaid\n")
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&source, "token%03d --> token%03d\n", i, i+1)
	}
	source.WriteString("```\nafter")
	h := formattingHarness(t, source.String(), 80, 15)
	h.input(chatKey(tea.KeyPgUp, 0))
	anchor := regexp.MustCompile(`token\d+`).FindString(ansi.Strip(h.m.View().Content))
	if anchor == "" {
		t.Fatal("scroll did not reveal a source anchor")
	}
	for _, width := range []int{32, 100, 20, 80} {
		h.input(tea.WindowSizeMsg{Width: width, Height: 15})
		if !strings.Contains(ansi.Strip(h.m.View().Content), anchor) {
			t.Errorf("resize to %d lost source anchor", width)
		}
		v2FrameFits(t, h, width, 15)
	}
}

func TestChatDiagramExportSentMessagesWithoutAcknowledging(t *testing.T) {
	base := diagramStateHome(t)
	h := newChatHarness(t, chat.Bounds{})
	msg := chatIncoming("sent-diagram", 7)
	msg.From, msg.To, msg.Origin = protocol.OperatorID, "author", protocol.OriginOperator
	msg.Payload, _ = json.Marshal(map[string]string{"text": "```mermaid\nA\n```"})
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	page := chatHistory(msg)
	page.History.Entries[0].Direction = "sent"
	h.reply(history, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(tea.WindowSizeMsg{Width: 80, Height: 30})
	path := diagramPath(base, "A", "mmd", 1)
	chatEqual(t, "sent message export", diagramCommand(h, "/diagram #7"), "diagram saved: "+path)
	chatEqual(t, "no implicit acknowledgment", len(h.m.State().QueuedAcks), 0)
	if len(h.requests) != 0 || len(h.wire.writes) != 0 {
		t.Fatal("export caused broker traffic")
	}
}

func TestChatDiagramTranscriptEscapesTerminalControls(t *testing.T) {
	text := "```mermaid\nA\x1b[2J\x1b]52;c;secret\a\u202e --> B\n```"
	h := formattingHarness(t, text, 120, 30)
	frame := h.m.View().Content
	for _, control := range []string{"\x1b[2J", "\x1b]52", "\a", "\u202e"} {
		if strings.Contains(frame, control) {
			t.Fatalf("diagram source injected terminal control %q", control)
		}
	}
	plain := ansi.Strip(frame)
	for _, escaped := range []string{`\x1b`, `\x07`, `\u202e`} {
		if !strings.Contains(plain, escaped) {
			t.Errorf("escaped source missing %q", escaped)
		}
	}
}
