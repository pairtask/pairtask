package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func chooseCommand(h *chatHarness, name string) {
	h.t.Helper()
	for range 20 {
		p := h.m.State().Picker
		if p == nil {
			h.t.Fatal("command picker is missing")
		}
		if p.Items[p.Selected] == name {
			return
		}
		h.input(chatKey(tea.KeyDown, 0))
	}
	h.t.Fatalf("command picker never offered %q", name)
}

func TestChatCommandPickerEnterRunsHighlightedAgentsFromEmptyLine(t *testing.T) {
	h := v3DisplayHarness(t, false)
	v2Type(h, "saved draft")
	h.input(chatKey('k', tea.ModCtrl))
	chooseCommand(h, "agents")
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "selected command ran", h.m.State().CommandLine, nil)
	v3Contains(t, ansi.Strip(h.m.View().Content), "Agents")
	chatEqual(t, "command preserves draft", h.m.State().Draft, "saved draft")
	chatEqual(t, "command sends nothing", h.m.State().Intent, nil)
}

func TestChatCommandPickerEnterRunsHighlightedAmbiguousPrefix(t *testing.T) {
	h := v3DisplayHarness(t, false)
	h.input(chatKey('k', tea.ModCtrl))
	v2Type(h, "c")
	chooseCommand(h, "clear")
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "selected clear executed", h.m.State().Cutoff, uint64(7))
	chatEqual(t, "executed command closed", h.m.State().CommandLine, nil)
	chatEqual(t, "clear keeps immutable history", len(h.m.State().Entries), 1)
}

func TestChatCommandPickerEnterCompletesRequiredArgumentCommand(t *testing.T) {
	h := v3DisplayHarness(t, false)
	h.input(chatKey('k', tea.ModCtrl))
	v2Type(h, "ch")
	h.input(chatKey(tea.KeyEnter, 0))
	line := h.m.State().CommandLine
	if line == nil {
		t.Fatal("argument-taking command closed before its argument")
	}
	chatEqual(t, "completed command leaves argument space", line.Text, "chat ")
	chatEqual(t, "completion does not switch conversation", h.m.State().SelectedAll, true)
	v2Type(h, "codex-1")
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "argument executes normally", h.m.State().Selected, "codex-1")
}

func TestChatCommandPickerTabStillOnlyCompletes(t *testing.T) {
	h := v3DisplayHarness(t, false)
	h.input(chatKey('k', tea.ModCtrl))
	chooseCommand(h, "agents")
	h.input(chatKey(tea.KeyTab, 0))
	line := h.m.State().CommandLine
	if line == nil {
		t.Fatal("Tab ran a command")
	}
	chatEqual(t, "Tab completed selection", strings.TrimSpace(line.Text), "agents")
	v3Contains(t, ansi.Strip(h.m.View().Content), "body target")
	h.input(chatKey(tea.KeyEscape, 0))
	chatEqual(t, "Esc closes command line", h.m.State().CommandLine, nil)
}

func TestChatCommandPickerCannotExecutePastedText(t *testing.T) {
	h := v3DisplayHarness(t, false)
	h.input(chatKey('k', tea.ModCtrl))
	h.input(tea.PasteMsg{Content: "agents"})
	h.input(chatKey(tea.KeyEnter, 0))
	if h.m.State().CommandLine == nil {
		t.Fatal("Enter executed pasted command text")
	}
	v3Contains(t, h.m.State().Status, "pasted text doesn't run")
	v3Contains(t, ansi.Strip(h.m.View().Content), "body target")
	chatEqual(t, "pasted command never sends", h.m.State().Intent, nil)
}

func TestChatCommandLineKeepsCursorVisibleInsideResizedBox(t *testing.T) {
	h := v3DisplayHarness(t, false)
	h.input(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.input(chatKey('k', tea.ModCtrl))
	command := "find " + strings.Repeat("x", 50) + "Ω" + strings.Repeat("y", 50) + "TAIL"
	v2Type(h, command)
	for _, width := range []int{80, 40, 20, 80} {
		h.input(tea.WindowSizeMsg{Width: width, Height: 24})
		v3Contains(t, ansi.Strip(h.m.View().Content), "TAIL")
		v2FrameFits(t, h, width, 24)
		chatEqual(t, "resize preserves full command", h.m.State().CommandLine.Text, command)
	}
	h.input(chatKey(tea.KeyHome, 0))
	v3Contains(t, ansi.Strip(h.m.View().Content), "/find")
	for range 55 {
		h.input(chatKey(tea.KeyRight, 0))
	}
	h.input(tea.WindowSizeMsg{Width: 40, Height: 24})
	v3Contains(t, ansi.Strip(h.m.View().Content), "Ω")
	h.input(chatKey(tea.KeyEnd, 0))
	v3Contains(t, ansi.Strip(h.m.View().Content), "TAIL")
}
