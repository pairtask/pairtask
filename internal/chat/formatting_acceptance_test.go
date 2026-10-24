package chat_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
)

func formattingHarness(t *testing.T, text string, width, height int) *chatHarness {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	msg := chatIncoming("formatted", 7)
	msg.Payload, _ = json.Marshal(map[string]string{"text": text})
	v2History(h, msg)
	h.input(tea.WindowSizeMsg{Width: width, Height: height})
	return h
}

func formattingRow(t *testing.T, h *chatHarness, needle string) string {
	t.Helper()
	for _, row := range strings.Split(h.m.View().Content, "\n") {
		if strings.Contains(ansi.Strip(row), needle) {
			return row
		}
	}
	t.Fatalf("text %q absent from frame: %q", needle, ansi.Strip(h.m.View().Content))
	return ""
}

func formattingRowsBetween(t *testing.T, h *chatHarness, first, last string) []string {
	t.Helper()
	rows := strings.Split(ansi.Strip(h.m.View().Content), "\n")
	start := -1
	for i, row := range rows {
		if start < 0 && strings.Contains(row, first) {
			start = i
		}
		if start >= 0 && strings.Contains(row, last) {
			return rows[start : i+1]
		}
	}
	t.Fatalf("range %q through %q absent from %q", first, last, rows)
	return nil
}

func TestChatFormattingParagraphBreaksCollapseWithoutJoiningLines(t *testing.T) {
	h := formattingHarness(t, "First paragraph\n\n\nSecond paragraph\nHard break", 80, 30)
	rows := formattingRowsBetween(t, h, "First paragraph", "Hard break")
	for i := range rows {
		rows[i] = strings.TrimSpace(rows[i])
	}
	chatEqual(t, "one blank row separates paragraphs", strings.Join(rows, "\n"),
		"First paragraph\n\nSecond paragraph\nHard break")
}

func TestChatFormattingListContinuationAlignsUnderItemText(t *testing.T) {
	for _, marker := range []string{"- ", "* ", "• ", "1. ", "12) ", "  - "} {
		t.Run(marker, func(t *testing.T) {
			for _, width := range []int{20, 32} {
				h := formattingHarness(t, marker+"alpha bravo charlie delta echo foxtrot golf hotel TAIL", width, 40)
				rows := formattingRowsBetween(t, h, "alpha", "TAIL")
				chatEqual(t, "list marker remains visible", strings.HasPrefix(rows[0], "  "+marker), true)
				if len(rows) < 2 {
					t.Fatal("fixture did not exercise wrapping")
				}
				indent := 2 + ansi.StringWidth(marker)
				for _, row := range rows[1:] {
					chatEqual(t, "continuation indent", len(row)-len(strings.TrimLeft(row, " ")), indent)
				}
				v2FrameFits(t, h, width, 40)
			}
		})
	}
}

func TestChatFormattingHeadingAndInlineStylesHaveBoundaries(t *testing.T) {
	h := formattingHarness(t, "## Heading\nplain **bold** plain *italic* plain _slant_ plain __strong__ plain <u>underlined</u> plain `code` plain", 160, 25)
	heading := formattingRow(t, h, "Heading")
	chatEqual(t, "heading marker removed", strings.TrimSpace(ansi.Strip(heading)), "Heading")
	chatEqual(t, "heading bold", v3StyleAt(t, heading, "Heading").Attrs&uv.AttrBold != 0, true)
	row := formattingRow(t, h, "underlined")
	chatEqual(t, "inline markers removed", strings.TrimSpace(ansi.Strip(row)), "plain bold plain italic plain slant plain strong plain underlined plain code plain")
	for _, word := range []string{"bold", "strong"} {
		chatEqual(t, word+" is bold", v3StyleAt(t, row, word).Attrs&uv.AttrBold != 0, true)
	}
	for _, word := range []string{"italic", "slant"} {
		chatEqual(t, word+" is italic", v3StyleAt(t, row, word).Attrs&uv.AttrItalic != 0, true)
	}
	chatEqual(t, "underline", v3StyleAt(t, row, "underlined").Underline != 0, true)
	plain := v3StyleAt(t, row, "plain")
	chatEqual(t, "plain text has no bold/italic", plain.Attrs&(uv.AttrBold|uv.AttrItalic), uint8(0))
	chatEqual(t, "code has distinct style", reflect.DeepEqual(v3StyleAt(t, row, "code"), plain), false)
}

func TestChatFormattingUnbalancedAndUnsupportedSyntaxStaysLiteral(t *testing.T) {
	for _, text := range []string{"**unfinished", "`unfinished", "<u>unfinished", "agent_id and file_name.go", "#123 issue", "[label](https://example.test)", "<script>literal</script>"} {
		t.Run(text, func(t *testing.T) {
			h := formattingHarness(t, text, 100, 20)
			chatEqual(t, "literal text preserved", strings.TrimSpace(ansi.Strip(formattingRow(t, h, text))), text)
		})
	}
}

func TestChatFormattingInlineCodeDoesNotInterpretOtherMarkers(t *testing.T) {
	h := formattingHarness(t, "before `**literal** <u>tag</u> _name_` after", 100, 25)
	row := formattingRow(t, h, "literal")
	chatEqual(t, "only code delimiters removed", strings.TrimSpace(ansi.Strip(row)), "before **literal** <u>tag</u> _name_ after")
	style := v3StyleAt(t, row, "literal")
	chatEqual(t, "code not bold", style.Attrs&uv.AttrBold != 0, false)
	chatEqual(t, "code not italic", style.Attrs&uv.AttrItalic != 0, false)
	chatEqual(t, "code not underlined", style.Underline != 0, false)
	chatEqual(t, "style does not leak after code", reflect.DeepEqual(v3StyleAt(t, row, "before"), v3StyleAt(t, row, "after")), true)
}

func TestChatFormattingListPrefixesCannotInjectControlsOrHideText(t *testing.T) {
	for _, prefix := range []string{"\t- ", "\r- ", strings.Repeat(" ", 40) + "- "} {
		h := formattingHarness(t, prefix+"VISIBLE TAIL", 20, 40)
		frame := h.m.View().Content
		chatEqual(t, "prefix has no raw controls", strings.ContainsAny(frame, "\r\t"), false)
		chatEqual(t, "indent cannot hide item", strings.Contains(ansi.Strip(frame), "TAIL"), true)
		v2FrameFits(t, h, 20, 40)
	}
}

func TestChatFormattingFencedCodePreservesIndentAndLiteralMarkers(t *testing.T) {
	for _, closeFence := range []string{"\n```", ""} {
		h := formattingHarness(t, "```go\n    **literal** <u>tag</u> `ticks`\n\t- item"+closeFence, 100, 25)
		chatEqual(t, "code indentation and markers", ansi.Strip(formattingRow(t, h, "**literal**")), "      **literal** <u>tag</u> `ticks`")
		chatEqual(t, "tab expanded in code", ansi.Strip(formattingRow(t, h, "- item")), "      - item")
		chatEqual(t, "fence hidden", strings.Contains(ansi.Strip(h.m.View().Content), "```"), false)
		chatEqual(t, "code is not bold", v3StyleAt(t, formattingRow(t, h, "**literal**"), "literal").Attrs&uv.AttrBold != 0, false)
	}
}

func TestChatFormattingLongCodeKeepsEveryCharacter(t *testing.T) {
	code := "CODESTART" + strings.Repeat("0123456789", 18) + "CODEEND"
	h := formattingHarness(t, "```\n"+code+"\n```", 32, 35)
	rows := formattingRowsBetween(t, h, "CODESTART", "CODEEND")
	for i := range rows {
		rows[i] = strings.TrimPrefix(rows[i], "  ")
	}
	chatEqual(t, "hard wrapping never clips code", strings.Join(rows, ""), code)
	v2FrameFits(t, h, 32, 35)
}

func TestChatFormattingCopyKeepsOriginalPayload(t *testing.T) {
	text := "## Heading\n\n- **bold** and <u>underline</u>\n```\n  raw\n```"
	h := formattingHarness(t, text, 80, 30)
	h.input(chatKey('k', tea.ModCtrl))
	v2Type(h, "copy #7")
	cmd := h.update(chatKey(tea.KeyEnter, 0))
	if cmd == nil {
		t.Fatal("copy returned no clipboard command")
	}
	chatEqual(t, "clipboard receives original text", cmd(), tea.SetClipboard(text)())
}

func TestChatFormattingFindMatchesVisibleAndRawText(t *testing.T) {
	h := formattingHarness(t, "**ready** now\n<u>next</u> step", 80, 25)
	for _, query := range []string{"ready now", "**ready**", "next step", "<u>next</u>"} {
		v2Command(h, "/find "+query)
		chatEqual(t, "find "+query, strings.Contains(h.m.State().Status, "found #7"), true)
	}
}

func TestChatFormattingEscapesControlsBeforeStyling(t *testing.T) {
	attack := "PAYLOAD\x1b[2J\x1b]52;c;secret\a\rOVERWRITE\u202e"
	for _, text := range []string{"**" + attack + "**", "`" + attack + "`", "# " + attack, "- " + attack, "```\n" + attack + "\n```"} {
		h := formattingHarness(t, text, 160, 25)
		frame := h.m.View().Content
		chatEqual(t, "no screen control", strings.Contains(frame, "\x1b[2J"), false)
		chatEqual(t, "no clipboard control", strings.Contains(frame, "\x1b]52"), false)
		chatEqual(t, "no raw controls", strings.ContainsAny(frame, "\r\a\u202e"), false)
		plain := ansi.Strip(frame)
		for _, escaped := range []string{`\x1b`, `\x07`, `\u202e`} {
			chatEqual(t, "control remains visible "+escaped, strings.Contains(plain, escaped), true)
		}
	}
}

func TestChatFormattingResizeKeepsAnchorInsideStyledList(t *testing.T) {
	var text strings.Builder
	text.WriteString("12. ")
	for i := range 400 {
		fmt.Fprintf(&text, "**token%03d** 界 ", i)
	}
	h := formattingHarness(t, text.String(), 80, 12)
	h.input(chatKey(tea.KeyPgUp, 0))
	before := ansi.Strip(h.m.View().Content)
	anchor := regexp.MustCompile(`token\d+`).FindString(before)
	if anchor == "" {
		t.Fatal("no anchor in fixture")
	}
	for _, width := range []int{32, 100, 20, 80} {
		h.input(tea.WindowSizeMsg{Width: width, Height: 12})
		chatEqual(t, "resize preserves reading token", strings.Contains(ansi.Strip(h.m.View().Content), anchor), true)
		v2FrameFits(t, h, width, 12)
	}
}
