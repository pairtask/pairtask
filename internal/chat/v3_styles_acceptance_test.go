package chat_test

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Decode terminal cells instead of depending on ANSI parameter ordering.
func v3StyledRow(row string) uv.ScreenBuffer {
	screen := uv.NewScreenBuffer(ansi.StringWidth(row), 1)
	uv.NewStyledString(row).Draw(screen, screen.Bounds())
	return screen
}

func v3StyleAt(t *testing.T, row, text string) uv.Style {
	t.Helper()
	plain := ansi.Strip(row)
	at := strings.Index(plain, text)
	if at < 0 {
		t.Fatalf("text %q absent from %q", text, plain)
	}
	col := ansi.StringWidth(plain[:at])
	return v3StyledRow(row).CellAt(col, 0).Style
}

func v3ColorEqual(t *testing.T, label string, got, want color.Color) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s has no colour", label)
	}
	if want == nil {
		t.Fatalf("%s reference has no colour", label)
	}
	chatEqual(t, label, color.NRGBAModel.Convert(got), color.NRGBAModel.Convert(want))
}

func TestChatV3StylesHeaderBandSpansEveryCell(t *testing.T) {
	h := v3DisplayHarness(t, false)
	row := strings.Split(h.m.View().Content, "\n")[0]
	cells := v3StyledRow(row).Lines[0]
	chatEqual(t, "header band width", len(cells), 120)
	first := cells[0].Style
	if first.Bg == nil {
		t.Fatal("header has no background band")
	}
	chatEqual(t, "title is bold", first.Attrs&uv.AttrBold != 0, true)
	formattingReadableStyle(t, "header", first)
	for _, cell := range cells {
		v3ColorEqual(t, "continuous header background", cell.Style.Bg, first.Bg)
		v3ColorEqual(t, "continuous header foreground", cell.Style.Fg, first.Fg)
	}
}

func TestChatV3StylesAudienceUsesBroadcastAndErrorColors(t *testing.T) {
	h := v3DisplayHarness(t, false)
	warning := v3StyleAt(t, v3AudienceRow(t, h), "to everyone")
	ack := v3StyleAt(t, formattingRow(t, h, "needs ack"), "needs ack")
	v3ColorEqual(t, "broadcast uses warning colour", warning.Fg, ack.Fg)
	chatEqual(t, "broadcast warning is bold", warning.Attrs&uv.AttrBold != 0, true)
	v2Type(h, "@all @codex-1 body")
	invalid := v3StyleAt(t, v3AudienceRow(t, h), "✗")
	if invalid.Fg == nil {
		t.Fatal("blocking reason has no error colour")
	}
	errorColor := color.NRGBAModel.Convert(invalid.Fg).(color.NRGBA)
	chatEqual(t, "blocking reason remains red", errorColor.R > errorColor.G && errorColor.R > errorColor.B, true)
}

func TestChatV3StylesDirectAndReplyMatchSenderColor(t *testing.T) {
	for _, command := range []string{`/to ["codex-1"]`, "/reply #7"} {
		t.Run(command, func(t *testing.T) {
			h := v3DisplayHarness(t, false)
			var sender color.Color
			for _, row := range strings.Split(h.m.View().Content, "\n") {
				if strings.Contains(ansi.Strip(row), "codex-1 → you") {
					sender = v3StyleAt(t, row, "codex-1").Fg
				}
			}
			v2Command(h, command)
			actual := v3StyleAt(t, v3AudienceRow(t, h), "codex-1")
			v3ColorEqual(t, "audience matches sender", actual.Fg, sender)
		})
	}
}

func TestChatV3StylesCommandLabelIsDistinctFromAudience(t *testing.T) {
	h := v3DisplayHarness(t, false)
	h.input(chatKey('k', tea.ModCtrl))
	style := v3StyleAt(t, v3AudienceRow(t, h), "command")
	chatEqual(t, "command label is bold", style.Attrs&uv.AttrBold != 0, true)
	chatEqual(t, "command uses default foreground", style.Fg == nil, true)
}
