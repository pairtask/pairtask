package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The terminal owns the text and background colours in both input fields.
// A dark, explicit cursor-line background makes default dark text disappear
// on a light terminal even though the draft is still present in the model.
func TestInputContrastAcceptanceTypedDraftAndCommandInheritTerminalColours(t *testing.T) {
	h := v3DisplayHarness(t, false)
	v2Type(h, "visible composer text")
	chatEqual(t, "draft remains editable", h.m.State().Draft, "visible composer text")
	assertInputInheritsTerminal(t, h.m.View().Content, "visible composer text")

	h.input(chatKey('k', tea.ModCtrl))
	v2Type(h, "styleprobe")
	assertInputInheritsTerminal(t, h.m.View().Content, "/styleprobe")
}

func assertInputInheritsTerminal(t *testing.T, frame, text string) {
	t.Helper()
	for _, row := range strings.Split(frame, "\n") {
		if !strings.Contains(ansi.Strip(row), text) {
			continue
		}
		style := v3StyleAt(t, row, text)
		if style.Fg != nil || style.Bg != nil {
			t.Fatalf("input %q overrides terminal text/background colours: fg=%v bg=%v", text, style.Fg, style.Bg)
		}
		return
	}
	t.Fatalf("typed input %q missing from rendered frame", text)
}
