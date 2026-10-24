package chat_test

import (
	"image/color"
	"math"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

func formattingLuminance(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	linear := func(v uint32) float64 {
		x := float64(v) / 65535
		if x <= 0.04045 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(r) + 0.7152*linear(g) + 0.0722*linear(b)
}

func formattingReadableStyle(t *testing.T, label string, style uv.Style) {
	t.Helper()
	if style.Fg == nil || style.Bg == nil {
		t.Fatalf("%s needs an explicit foreground/background pair", label)
	}
	chatEqual(t, label+" must not be faint", style.Attrs&uv.AttrFaint != 0, false)
	fg, bg := formattingLuminance(style.Fg), formattingLuminance(style.Bg)
	contrast := (math.Max(fg, bg) + 0.05) / (math.Min(fg, bg) + 0.05)
	if contrast < 4.5 {
		t.Fatalf("%s contrast %.2f, want at least 4.5", label, contrast)
	}
}

func formattingWarningStyle(t *testing.T, style uv.Style) {
	t.Helper()
	formattingReadableStyle(t, "warning", style)
	c := color.NRGBAModel.Convert(style.Fg).(color.NRGBA)
	chatEqual(t, "warning retains a warm distinct colour", c.R >= c.G && c.G > c.B, true)
	chatEqual(t, "warning remains bold", style.Attrs&uv.AttrBold != 0, true)
}

func TestChatFormattingHighlightedCommandExplanationHasReadableContrast(t *testing.T) {
	h := v3DisplayHarness(t, false)
	h.input(tea.WindowSizeMsg{Width: 150, Height: 30})
	h.input(chatKey('k', tea.ModCtrl))
	chooseCommand(h, "ack")
	row := formattingRow(t, h, "acknowledge:")
	label := v3StyleAt(t, row, "/ack")
	explanation := v3StyleAt(t, row, "acknowledge:")
	formattingReadableStyle(t, "selected command", label)
	formattingReadableStyle(t, "selected explanation", explanation)
	v3ColorEqual(t, "selected explanation foreground", explanation.Fg, label.Fg)
	v3ColorEqual(t, "selected explanation background", explanation.Bg, label.Bg)
}

func TestChatFormattingHeaderAndFooterHaveReadableContrast(t *testing.T) {
	h := v3DisplayHarness(t, false)
	formattingReadableStyle(t, "title band", v3StyleAt(t, formattingRow(t, h, "pairtask operator chat"), "pairtask operator chat"))
	formattingReadableStyle(t, "footer band", v3StyleAt(t, formattingRow(t, h, "connected"), "connected"))
}

func TestChatFormattingSelectedConversationUsesReadableBand(t *testing.T) {
	h := v3DisplayHarness(t, false)
	for _, name := range []string{"All", "codex-1"} {
		if name != "All" {
			v2Command(h, "/chat "+name)
		}
		row := formattingRow(t, h, "1 All")
		formattingReadableStyle(t, "selected conversation "+name, v3StyleAt(t, row, name))
	}
}
