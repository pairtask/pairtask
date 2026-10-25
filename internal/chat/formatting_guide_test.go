package chat_test

import (
	"os"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Keep the guide's pasteable example tied to the actual chat renderer.
func TestFormattingGuideExampleRenders(t *testing.T) {
	data, err := os.ReadFile("testdata/formatting-guide-message.txt")
	if err != nil {
		t.Fatal(err)
	}
	h := formattingHarness(t, strings.TrimSuffix(string(data), "\n"), 120, 35)
	frame := ansi.Strip(h.m.View().Content)
	guideText(t, frame, true, []string{
		"Release check ✅", "- Build passes", "- Review is ready",
		"- Deploy after approval", "Item", "Owner", "Checks", "codex-1",
		"Notes", "README.md", "**literal** stays in the code block",
	})
	guideText(t, frame, false, []string{"## Release", "**Build**", "*Review*", "<u>Deploy</u>", "```text"})
	guideStyle(h, "Release check", "Release check", uv.AttrBold)
	guideStyle(h, "Build passes", "Build", uv.AttrBold)
	guideStyle(h, "Review is ready", "Review", uv.AttrItalic)
	guideStyle(h, "Item", "Item", uv.AttrBold)
	if v3StyleAt(t, formattingRow(t, h, "Deploy after approval"), "Deploy").Underline == 0 {
		t.Error("underlined list text is not underlined")
	}
	if v3StyleAt(t, formattingRow(t, h, "**literal**"), "literal").Attrs&uv.AttrBold != 0 {
		t.Error("code block interpreted bold markers")
	}
}

func guideText(t *testing.T, frame string, present bool, fragments []string) {
	t.Helper()
	for _, fragment := range fragments {
		if strings.Contains(frame, fragment) != present {
			t.Errorf("rendered example presence of %q: got %v, want %v", fragment, !present, present)
		}
	}
}

func guideStyle(h *chatHarness, rowNeedle, word string, attr uint8) {
	h.t.Helper()
	if v3StyleAt(h.t, formattingRow(h.t, h, rowNeedle), word).Attrs&attr == 0 {
		h.t.Errorf("%q lacks expected style", word)
	}
}

func TestFormattingGuideLiteralExamplesStayLiteral(t *testing.T) {
	for _, source := range []string{
		"[text](url)", "![alt](url)", "> block quotes",
		"<span>other HTML</span>", "file_name", "**unmatched",
	} {
		t.Run(source, func(t *testing.T) {
			h := formattingHarness(t, source, 100, 15)
			row := formattingRow(t, h, source)
			if !strings.Contains(ansi.Strip(row), source) {
				t.Fatalf("source %q changed to %q", source, ansi.Strip(row))
			}
		})
	}
}
