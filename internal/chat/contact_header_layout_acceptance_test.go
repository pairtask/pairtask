package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestContactHeaderLayoutAcceptanceShowsFullPendingIdentityAndAction(t *testing.T) {
	person := "sha256:" + strings.Repeat("c", 64)
	h, _ := contactKeyCommandHarness(t, person)
	for _, width := range []int{45, 80, 100} {
		h.input(tea.WindowSizeMsg{Width: width, Height: 32})
		v2FrameFits(t, h, width, 32)
		view := ansi.Strip(h.m.View().Content)
		if !strings.Contains(strings.Join(strings.Fields(view), ""), person) {
			t.Fatalf("width %d clipped the person identity:\n%s", width, view)
		}
		for _, instruction := range []string{"not yet accepted", "compare it with them", "/pair accept-key"} {
			if !strings.Contains(view, instruction) {
				t.Fatalf("width %d clipped %q:\n%s", width, instruction, view)
			}
		}
		action := strings.Index(view, "/pair accept-key")
		if action < 0 || !strings.Contains(view[action:], "────────") {
			t.Fatalf("width %d has no separator below the contact details:\n%s", width, view)
		}
	}
}
