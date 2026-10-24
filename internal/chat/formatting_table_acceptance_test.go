package chat_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func formattingTableColumns(t *testing.T, h *chatHarness) []string {
	t.Helper()
	var columns [2]strings.Builder
	rows := formattingRowsBetween(t, h, "┼", "needs ack")
	for _, row := range rows[1 : len(rows)-1] {
		cells := strings.Split(row, "│")
		if len(cells) != 2 {
			continue
		}
		for i, cell := range cells {
			columns[i].WriteString(strings.Join(strings.Fields(cell), ""))
		}
	}
	return []string{columns[0].String(), columns[1].String()}
}

func formattingNumberedWords(prefix string, count int) string {
	var text strings.Builder
	for i := range count {
		fmt.Fprintf(&text, "%s%03d ", prefix, i)
	}
	return text.String()
}

func formattingTwoColumnTable(left, right string) string {
	return "| Left | Right |\n| --- | --- |\n| " + left + " | " + right + " |"
}

func TestChatFormattingTableWrapsEveryCellAtNarrowWidths(t *testing.T) {
	left := "alpha123456789 omega987654321"
	right := "ready123456789 final987654321"
	text := "| Tool | State |\n| --- | --- |\n| " + left + " | " + right + " |"
	for _, width := range []int{20, 32, 80} {
		h := formattingHarness(t, text, width, 45)
		columns := formattingTableColumns(t, h)
		chatEqual(t, "first column loses no content", columns[0], strings.ReplaceAll(left, " ", ""))
		chatEqual(t, "second column loses no content", columns[1], strings.ReplaceAll(right, " ", ""))
		v2FrameFits(t, h, width, 45)
		row := formattingRow(t, h, "Tool")
		chatEqual(t, "table header bold", v3StyleAt(t, row, "Tool").Attrs&uv.AttrBold != 0, true)
	}
}

func TestChatFormattingTableCellsSupportInlineStyles(t *testing.T) {
	h := formattingHarness(t, "| Tool | State |\n| --- | --- |\n| **bold** _italic_ | <u>underline</u> `code` |", 100, 30)
	row := formattingRow(t, h, "underline")
	chatEqual(t, "cell bold", v3StyleAt(t, row, "bold").Attrs&uv.AttrBold != 0, true)
	chatEqual(t, "cell italic", v3StyleAt(t, row, "italic").Attrs&uv.AttrItalic != 0, true)
	chatEqual(t, "cell underline", v3StyleAt(t, row, "underline").Underline != 0, true)
	plain := ansi.Strip(row)
	for _, marker := range []string{"**", "_italic_", "<u>", "`"} {
		chatEqual(t, "table removes balanced marker", strings.Contains(plain, marker), false)
	}
}

func TestChatFormattingOrdinaryPipesRemainText(t *testing.T) {
	text := "run cat file | sort\nthen echo one | cat"
	h := formattingHarness(t, text, 100, 25)
	for _, line := range strings.Split(text, "\n") {
		chatEqual(t, "pipe without table separator", strings.TrimSpace(ansi.Strip(formattingRow(t, h, line))), line)
	}
}

func TestChatFormattingTooManyTableColumnsNeverLoseLastCell(t *testing.T) {
	text := "| A | B | C | D | E | F | G | TailHead |\n" +
		"| --- | --- | --- | --- | --- | --- | --- | --- |\n" +
		"| one | two | three | four | five | six | seven | TailValue |"
	h := formattingHarness(t, text, 20, 100)
	plain := ansi.Strip(h.m.View().Content)
	for _, word := range []string{"TailHead", "one", "two", "three", "four", "five", "six", "seven", "TailValue"} {
		chatEqual(t, "narrow table retains "+word, strings.Contains(plain, word), true)
	}
	v2FrameFits(t, h, 20, 100)
}

func TestChatFormattingEmojiSequencesStayIntact(t *testing.T) {
	for _, width := range []int{20, 32, 80} {
		h := formattingHarness(t, "- **Status** ✅ 👩‍💻 ❤️ 🇵🇱 👍🏽\n  More text after the emoji", width, 30)
		plain := ansi.Strip(h.m.View().Content)
		for _, emoji := range []string{"✅", "👩‍💻", "❤️", "🇵🇱", "👍🏽"} {
			chatEqual(t, "emoji grapheme preserved "+emoji, strings.Contains(plain, emoji), true)
		}
		v2FrameFits(t, h, width, 30)
	}
}

func TestChatFormattingTallTableRowsRemainReachableByScrolling(t *testing.T) {
	text := formattingTwoColumnTable(formattingNumberedWords("L", 60), formattingNumberedWords("R", 60))
	h := formattingHarness(t, text, 32, 12)
	var seen strings.Builder
	for range 35 {
		seen.WriteString(ansi.Strip(h.m.View().Content))
		seen.WriteByte('\n')
		h.input(chatKey(tea.KeyPgUp, 0))
		v2FrameFits(t, h, 32, 12)
	}
	for i := range 60 {
		for _, side := range []string{"L", "R"} {
			word := fmt.Sprintf("%s%03d", side, i)
			chatEqual(t, "scroll reaches "+word, strings.Contains(seen.String(), word), true)
		}
	}
}

func TestChatFormattingTableResizeKeepsTheReadingPosition(t *testing.T) {
	for _, tc := range []struct{ name, left, pattern string }{
		{"both columns long", formattingNumberedWords("L", 200), `L\d+`},
		{"later column long", "short", `R\d+`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := formattingTwoColumnTable(tc.left, formattingNumberedWords("R", 200))
			h := formattingHarness(t, text, 80, 12)
			h.input(chatKey(tea.KeyPgUp, 0))
			anchor := regexp.MustCompile(tc.pattern).FindString(ansi.Strip(h.m.View().Content))
			if anchor == "" {
				t.Fatal("fixture has no table reading anchor")
			}
			for _, width := range []int{32, 80} {
				h.input(tea.WindowSizeMsg{Width: width, Height: 12})
				chatEqual(t, "table resize preserves reading text", strings.Contains(ansi.Strip(h.m.View().Content), anchor), true)
				v2FrameFits(t, h, width, 12)
			}
		})
	}
}

func TestChatFormattingStackedTableDoesNotReinterpretCode(t *testing.T) {
	text := "| Header with ENDHEADER | B | C | D | E | F | G | H |\n" +
		"| --- | --- | --- | --- | --- | --- | --- | --- |\n" +
		"| `**literal**` | two | three | four | five | six | seven | **strong** |"
	h := formattingHarness(t, text, 20, 100)
	chatEqual(t, "stacked header stays readable", strings.Contains(ansi.Strip(h.m.View().Content), "ENDHEADER"), true)
	row := formattingRow(t, h, "literal")
	chatEqual(t, "code markers stay literal in stacked cells", strings.Contains(ansi.Strip(row), "**literal**"), true)
	chatEqual(t, "code not reparsed as bold", v3StyleAt(t, row, "literal").Attrs&uv.AttrBold != 0, false)
	chatEqual(t, "stacked cell keeps bold", v3StyleAt(t, formattingRow(t, h, "strong"), "strong").Attrs&uv.AttrBold != 0, true)
}

func TestChatFormattingExtraTableCellsRemainReadable(t *testing.T) {
	text := "| A | B |\n| --- | --- |\n| one | two | three | four | five | six | seven | TailValue |"
	h := formattingHarness(t, text, 20, 100)
	chatEqual(t, "extra cells not clipped", strings.Contains(ansi.Strip(h.m.View().Content), "TailValue"), true)
	v2FrameFits(t, h, 20, 100)
}
