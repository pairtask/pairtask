package chat

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// A pipe table is a row, a separator row (---|:--:|---) and more rows; a line
// with pipes but no separator under it is ordinary text. Columns are padded
// to the widest plain cell; when the table is wider than the transcript the
// widest columns shrink and their cells wrap over several visual rows, so no
// cell content is ever dropped.

func isTableRow(line string) bool {
	return strings.Count(line, "|") >= 1 && strings.TrimSpace(line) != ""
}

// cell is one table cell: its inline text and where it starts in the source row.
type cell struct {
	inline
	start int
}

// table renders the rows from lines[i] on and returns the last index consumed.
func (f *formatter) table(lines []string, i int) int {
	var rows [][]cell
	last := i
	for j := i; j < len(lines) && isTableRow(lines[j]); j++ {
		last = j
		if j == i+1 { // the separator row renders as a rule, below
			continue
		}
		rows = append(rows, cells(lines[j]))
	}
	avail := max(1, f.width-len(body))
	if columns := len(rows[0]); total(make([]int, columns))+columns > avail {
		f.stacked(rows) // even one-cell columns don't fit
		return last
	}
	widths := columnWidths(rows, avail)
	f.b.src = f.tableRow(rows[0], widths, boldStyle)
	f.rule(widths)
	for _, row := range rows[1:] {
		f.b.src = f.tableRow(row, widths, lipgloss.NewStyle())
	}
	return last
}

// stacked is the narrow fallback when even one-cell columns don't fit: each
// data row becomes "header: value" lines, nothing is dropped.
func (f *formatter) stacked(rows [][]cell) {
	header := rows[0]
	f.b.skip() // the header row itself
	f.b.skip() // the separator row
	for _, row := range rows[1:] {
		start := len(f.b.lines)
		for k, c := range row {
			name := ""
			if k < len(header) {
				name = header[k].plain + ": "
			}
			f.styledLine(name+c.plain, lead{first: "", rest: "  "}, lipgloss.NewStyle())
			f.b.src-- // every line of the row shares its src
		}
		for i := start; i < len(f.b.lines); i++ {
			f.b.lines[i].col = i - start // increasing within the row, like tableRow
		}
		f.b.src++
	}
}

// cells splits a row on every pipe, dropping the outer empty cells. Pipes
// inside code spans and backslash-escaped pipes are not special in this
// subset; a cell that needs a literal pipe is not supported.
func cells(line string) []cell {
	var out []cell
	pos := 0
	for _, field := range strings.Split(line, "|") {
		text := strings.TrimSpace(field)
		start := pos + strings.Index(field, text)
		pos += len(field) + 1
		out = append(out, cell{inline: inlineSpans(text), start: start})
	}
	if len(out) > 1 && out[0].plain == "" {
		out = out[1:]
	}
	if len(out) > 1 && out[len(out)-1].plain == "" {
		out = out[:len(out)-1]
	}
	return out
}

// columnWidths pads every column to its widest cell, then shrinks the widest
// columns one cell at a time until the row fits in width.
func columnWidths(rows [][]cell, width int) []int {
	var widths []int
	for _, row := range rows {
		for k, c := range row {
			for len(widths) <= k {
				widths = append(widths, 1)
			}
			widths[k] = max(widths[k], ansi.StringWidth(c.plain))
		}
	}
	for total(widths) > width {
		widest := 0
		for k := range widths {
			if widths[k] > widths[widest] {
				widest = k
			}
		}
		if widths[widest] <= 1 {
			break
		}
		widths[widest]--
	}
	return widths
}

func total(widths []int) int {
	sum := 3 * (len(widths) - 1) // " │ " between columns
	for _, w := range widths {
		sum += w
	}
	return sum
}

// tableRow renders one source row as as many visual rows as its tallest
// wrapped cell needs; all of them carry the row's src, and col is the source
// offset of the first column's text on that visual row, so a resize anchor
// finds the same text again. Returns the next src.
func (f *formatter) tableRow(row []cell, widths []int, base lipgloss.Style) int {
	wrapped := make([][]string, len(widths))
	var offsets []int
	height := 1
	for k := range widths {
		wrapped[k] = []string{""}
		if k < len(row) {
			var at []int
			wrapped[k], at = wrapCell(row[k].inline, widths[k], base)
			if k == 0 {
				offsets = at
			}
		}
		height = max(height, len(wrapped[k]))
	}
	for line := 0; line < height; line++ {
		var out []string
		for k := range widths {
			out = append(out, cellLine(wrapped[k], line, widths[k]))
		}
		f.b.lines = append(f.b.lines, renderLine{id: f.b.id, src: f.b.src, col: rowOffset(row, offsets, line), text: string(body) + strings.Join(out, ruleStyle.Render(" │ "))})
	}
	return f.b.src + 1
}

// rowOffset is the source offset of the first column's text on a visual row;
// rows past that column's text keep counting so the order stays increasing.
func rowOffset(row []cell, offsets []int, line int) int {
	if len(row) == 0 {
		return line
	}
	if line < len(offsets) {
		return row[0].start + row[0].origin(offsets[line])
	}
	return row[0].start + len(row[0].plain) + line
}

// wrapCell wraps a cell's plain text to the column width and styles each
// part; it also returns each part's plain offset.
func wrapCell(c inline, width int, base lipgloss.Style) ([]string, []int) {
	parts := strings.Split(ansi.Hardwrap(ansi.Wordwrap(c.plain, width, ""), width, true), "\n")
	out := make([]string, 0, len(parts))
	offsets := make([]int, 0, len(parts))
	pos := 0
	for _, part := range parts {
		if at := strings.Index(c.plain[pos:], part); at >= 0 {
			pos += at
		}
		out = append(out, c.render(pos, pos+len(part), base)+strings.Repeat(" ", max(0, width-ansi.StringWidth(part))))
		offsets = append(offsets, pos)
		pos += len(part)
	}
	return out, offsets
}

func cellLine(lines []string, i, width int) string {
	if i < len(lines) {
		return lines[i]
	}
	return strings.Repeat(" ", width)
}

// rule draws the separator row under the header.
func (f *formatter) rule(widths []int) {
	parts := make([]string, len(widths))
	for k, w := range widths {
		parts[k] = strings.Repeat(rule, w)
	}
	f.b.lines = append(f.b.lines, renderLine{id: f.b.id, src: f.b.src, col: 0, text: string(body) + ruleStyle.Render(strings.Join(parts, "─┼─"))})
	f.b.src++
}
