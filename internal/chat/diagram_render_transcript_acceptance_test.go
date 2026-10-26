package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestChatDiagramRenderKeepsCopyFindAndExportSource(t *testing.T) {
	base := diagramStateHome(t)
	source := "flowchart LR\nA[Sender] --> B[Receiver]"
	text := "before\n```mermaid\n" + source + "\n```\nafter"
	h := formattingHarness(t, text, 100, 40)
	frame := ansi.Strip(h.m.View().Content)
	if !strings.Contains(frame, " · rendered") {
		t.Fatalf("diagram did not render: %s", frame)
	}
	if strings.Contains(frame, "flowchart LR") {
		t.Fatal("diagram still displays raw source")
	}
	h.input(chatKey('k', tea.ModCtrl))
	v2Type(h, "copy #7")
	cmd := h.update(chatKey(tea.KeyEnter, 0))
	if cmd == nil {
		t.Fatal("copy command missing")
	}
	chatEqual(t, "copy rendered diagram source", cmd(), tea.SetClipboard(text)())
	for _, query := range []string{"flowchart LR", "A[Sender]", "mermaid"} {
		v2Command(h, "/find "+query)
		if !strings.Contains(h.m.State().Status, "found #7") {
			t.Errorf("hidden source not found: %s", query)
		}
	}
	path := (diagramExportName{base, source, "mmd", 1}).path()
	chatEqual(t, "export rendered diagram source", diagramCommand(h, "/diagram #7"), "diagram saved: "+path)
	chatEqual(t, "export file bytes", string(diagramRead(t, path)), source+"\n")
}

func TestChatDiagramRenderResizeSwitchesBetweenDiagramAndSource(t *testing.T) {
	text := "```mermaid\nflowchart LR\nA[BeginningOfJourney]\nB[DestinationReached]\nA --> B\n```"
	h := formattingHarness(t, text, 100, 40)
	for _, width := range []int{100, 32, 100} {
		h.input(tea.WindowSizeMsg{Width: width, Height: 40})
		frame := ansi.Strip(h.m.View().Content)
		wantRendered := width == 100
		if strings.Contains(frame, "flowchart LR") == wantRendered {
			t.Errorf("width %d did not switch source visibility", width)
		}
		if strings.Contains(frame, " · rendered") != wantRendered {
			t.Errorf("width %d render selection wrong: %s", width, frame)
		}
		for _, label := range []string{"BeginningOfJourney", "DestinationReached"} {
			if !strings.Contains(frame, label) {
				t.Errorf("width %d lost %s", width, label)
			}
		}
		v2FrameFits(t, h, width, 40)
	}
}

func TestChatDiagramRenderMixedBlocksKeepExportIndices(t *testing.T) {
	diagramStateHome(t)
	text := "```mermaid\nflowchart TD\nA --> B\n```\n```plantuml\nAlice -> Bob\n```\n```mermaid\npie\nA: 1\n```"
	h := formattingHarness(t, text, 100, 60)
	frame := ansi.Strip(h.m.View().Content)
	for _, label := range []string{"/diagram #7 1 · rendered", "/diagram #7 2", "/diagram #7 3"} {
		if !strings.Contains(frame, label) {
			t.Errorf("missing diagram index/label: %s", label)
		}
	}
	if strings.Count(frame, " · rendered") != 1 {
		t.Errorf("PlantUML or unsupported Mermaid rendered: %s", frame)
	}
	chatEqual(t, "ambiguous export remains explicit", diagramCommand(h, "/diagram #7"), "#7 has 3 diagrams: /diagram #7 K")
}
