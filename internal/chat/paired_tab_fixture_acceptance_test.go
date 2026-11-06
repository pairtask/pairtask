package chat_test

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func pairedTabHarness(t *testing.T) (*chatHarness, string) {
	t.Helper()
	h, key := pairedMentionsChat(t, true)
	h.input(tea.WindowSizeMsg{Width: 160, Height: 60})
	return h, key
}
func pairedTabView(h *chatHarness) string { return ansi.Strip(h.m.View().Content) }
func pairedTabChoice(items []string, forms ...string) int {
	for _, form := range forms {
		if index := slices.Index(items, form); index >= 0 {
			return index
		}
	}
	return -1
}
func pairedTabPick(t *testing.T, h *chatHarness, forms ...string) {
	t.Helper()
	p := h.m.State().Picker
	if p == nil {
		t.Fatal("mention picker is missing")
	}
	index := pairedTabChoice(p.Items, forms...)
	if index < 0 {
		t.Fatalf("picker lacks %q: %q", forms, p.Items)
	}
	for p.Selected < index {
		h.input(chatKey(tea.KeyDown, 0))
		p = h.m.State().Picker
	}
	for p.Selected > index {
		h.input(chatKey(tea.KeyUp, 0))
		p = h.m.State().Picker
	}
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "choosing a mention does not send", h.m.State().InFlight, 0)
}
func pairedTabEditing(t *testing.T, h *chatHarness, unfinished string) {
	t.Helper()
	hint := h.m.State().Hint
	if !strings.Contains(strings.ToLower(hint), "editing") {
		t.Errorf("unfinished addressing lacks an editing marker: %q", hint)
	}
	if strings.Contains(hint, unfinished+"@Office") {
		t.Errorf("header resolves unfinished word as recipient: %q", hint)
	}
	chatEqual(t, "editing preview sends nothing", h.m.State().InFlight, 0)
}
