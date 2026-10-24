package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
)

// Inspect the actual bottom input area, not the model's logical line count.
func growthComposerRows(t *testing.T, h *chatHarness) []string {
	t.Helper()
	rows := strings.Split(ansi.Strip(h.m.View().Content), "\n")
	for i, row := range rows {
		if isAudienceRow(row) {
			return composerBody(rows, i+1)
		}
	}
	t.Fatalf("composer audience line missing: %q", rows)
	return nil
}

func composerBody(rows []string, start int) []string {
	end := len(rows) - 1 // Footer stays below the input and picker.
	for i := start; i < end; i++ {
		if strings.HasPrefix(rows[i], "╰") {
			end = i
			break
		}
	}
	return rows[start:end]
}

func growthVisibleMarkers(t *testing.T, rows []string, markers ...string) {
	t.Helper()
	frame := strings.Join(rows, "\n")
	for _, marker := range markers {
		if !strings.Contains(frame, marker) {
			t.Fatalf("draft marker %q hidden before the six-row cap: %q", marker, frame)
		}
	}
}

func TestChatComposerGrowthKeepsWrappedDraftAndNewlineVisible(t *testing.T) {
	for _, newline := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"ctrl-j", chatKey('j', tea.ModCtrl)},
		{"alt-enter", chatKey(tea.KeyEnter, tea.ModAlt)},
		{"shift-enter", chatKey(tea.KeyEnter, tea.ModShift)},
	} {
		t.Run(newline.name, func(t *testing.T) {
			h := newChatHarness(t, chat.Bounds{})
			h.ready()
			h.input(tea.WindowSizeMsg{Width: 40, Height: 20})
			chatEqual(t, "empty input height", len(growthComposerRows(t, h)), 1)
			first := "START first words of a long draft that wraps beyond forty columns END"
			v2Type(h, first)
			rows := growthComposerRows(t, h)
			if len(rows) <= 1 {
				t.Fatalf("wrapped draft must grow beyond one row: %q", rows)
			}
			growthVisibleMarkers(t, rows, "START", "END")
			h.input(newline.key)
			v2Type(h, "NEXT")
			growthVisibleMarkers(t, growthComposerRows(t, h), "START", "END", "NEXT")
			chatEqual(t, "newline preserves draft", h.m.State().Draft, first+"\nNEXT")
			chatEqual(t, "newline does not send", h.m.State().Intent, nil)
			v2FrameFits(t, h, 40, 20)
		})
	}
}

func TestChatComposerGrowthCapsViewportWithoutCappingDraft(t *testing.T) {
	for _, pasted := range []bool{false, true} {
		name := "typed"
		if pasted {
			name = "pasted"
		}
		t.Run(name, func(t *testing.T) {
			h := newChatHarness(t, chat.Bounds{})
			h.ready()
			h.input(tea.WindowSizeMsg{Width: 60, Height: 24})
			draft := "row01\nrow02\nrow03\nrow04\nrow05\nrow06\nrow07\nrow08\nrow09"
			if pasted {
				h.input(tea.PasteMsg{Content: draft})
			} else {
				v2Type(h, draft)
			}
			chatEqual(t, "full draft retained", h.m.State().Draft, draft)
			rows := growthComposerRows(t, h)
			chatEqual(t, "input capped at six visual rows", len(rows), 6)
			if !strings.Contains(strings.Join(rows, "\n"), "row09") {
				t.Fatalf("cursor row hidden: %q", rows)
			}
			v2FrameFits(t, h, 60, 24)
			req := v2Send(h)
			chatEqual(t, "all rows sent", v2Body(t, req), draft)
			chatEqual(t, "empty input shrinks after send", len(growthComposerRows(t, h)), 1)
		})
	}
}

func TestChatComposerGrowthReflowsOnResizeAndShrinksAfterDeletion(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	h.input(tea.WindowSizeMsg{Width: 32, Height: 20})
	draft := "START " + strings.Repeat("word ", 12) + "END"
	v2Type(h, draft)
	narrow := len(growthComposerRows(t, h))
	h.input(tea.WindowSizeMsg{Width: 100, Height: 20})
	chatEqual(t, "wide input uses one row", len(growthComposerRows(t, h)), 1)
	h.input(tea.WindowSizeMsg{Width: 32, Height: 20})
	chatEqual(t, "narrow input grows again", len(growthComposerRows(t, h)), narrow)
	if narrow <= 1 {
		t.Fatal("narrow draft never wrapped")
	}
	for range len(draft) - len("START") {
		h.input(chatKey(tea.KeyBackspace, 0))
	}
	chatEqual(t, "remaining draft", h.m.State().Draft, "START")
	chatEqual(t, "deleted input shrinks", len(growthComposerRows(t, h)), 1)
	v2FrameFits(t, h, 32, 20)
}
