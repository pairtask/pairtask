package chat

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// A pipe table is a row, a separator row (---|:---:|---) and more rows; a line
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
	if columns := columnCount(rows); total(make([]int, columns))+columns > avail {
		f.stacked(rows) // even one-cell columns don't fit
		return last
	}
	grid := tableLayout{widths: columnWidths(rows, avail)}
	f.b.src = f.tableRow(rows[0], grid.with(boldStyle))
	f.rule(grid.widths)
	for _, row := range rows[1:] {
		f.b.src = f.tableRow(row, grid)
	}
	return last
}

// tableLayout is the column widths a table is laid out at and the style its
// cells start from.
type tableLayout struct {
	widths []int
	base   lipgloss.Style
}

func (l tableLayout) with(base lipgloss.Style) tableLayout {
	return tableLayout{widths: l.widths, base: base}
}

func columnCount(rows [][]cell) int {
	n := 0
	for _, row := range rows {
		n = max(n, len(row))
	}
	return n
}

// stacked is the narrow fallback when even one-cell columns don't fit: each
// data row becomes "header: value" lines, nothing is dropped.
func (f *formatter) stacked(rows [][]cell) {
	header := rows[0]
	f.b.skip() // the header row itself
	f.b.skip() // the separator row
	avail := max(1, f.width-len(body)-2)
	for _, row := range rows[1:] {
		for k, c := range row {
			name := ""
			if k < len(header) {
				name = header[k].plain + ": "
			}
			f.stackedCell(stackedField{name: name, cell: c, avail: avail})
		}
		f.b.src++
	}
}

// stackedField is one "name: value" of a stacked row and the width it has.
type stackedField struct {
	name  string
	cell  cell
	avail int
}

// stackedCell renders the field from the cell's own spans, the value wrapped
// under the name with a two-cell indent; when the name leaves too little
// room, the value starts on its own line. col is the source offset.
func (f *formatter) stackedCell(field stackedField) {
	first, width := boldStyle.Render(field.name), field.avail-ansi.StringWidth(field.name)
	if width < 8 {
		f.stackedName(field)
		first, width = "  ", field.avail
	}
	parts, offsets := wrapCell(field.cell.inline, max(1, width), lipgloss.NewStyle())
	for i, part := range parts {
		prefix := "  "
		if i == 0 {
			prefix = first
		}
		f.b.lines = append(f.b.lines, renderLine{id: f.b.id, src: f.b.src, col: field.cell.start + field.cell.origin(offsets[i]), text: string(body) + prefix + strings.TrimRight(part, " ")})
	}
}

// stackedName is a header name on rows of its own, wrapped.
func (f *formatter) stackedName(field stackedField) {
	for _, part := range wrap(field.name, field.avail) {
		f.b.lines = append(f.b.lines, renderLine{id: f.b.id, src: f.b.src, col: field.cell.start, text: string(body) + boldStyle.Render(strings.TrimRight(part, " "))})
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
	widths := widestCells(rows)
	for total(widths) > width {
		k := widest(widths)
		if widths[k] <= 1 {
			break
		}
		widths[k]--
	}
	return widths
}

func widestCells(rows [][]cell) []int {
	var widths []int
	for _, row := range rows {
		for k, c := range row {
			for len(widths) <= k {
				widths = append(widths, 1)
			}
			widths[k] = max(widths[k], ansi.StringWidth(c.plain))
		}
	}
	return widths
}

func widest(widths []int) int {
	k := 0
	for i := range widths {
		if widths[i] > widths[k] {
			k = i
		}
	}
	return k
}

func total(widths []int) int {
	sum := 3 * (len(widths) - 1) // " │ " between columns
	for _, w := range widths {
		sum += w
	}
	return sum
}

// wrappedRow is one source row wrapped into its columns: the visual lines
// per column and each line's plain offset within its cell.
type wrappedRow struct {
	row     []cell
	lines   [][]string
	offsets [][]int
}

// tableRow renders one source row as as many visual rows as its tallest
// wrapped cell needs; all of them carry the row's src, and col is the source
// offset of the leftmost cell that still has text on that visual row, so a
// resize anchor finds the same text again. Returns the next src.
func (f *formatter) tableRow(row []cell, grid tableLayout) int {
	w := grid.wrapRow(row)
	for line := 0; line < w.height(); line++ {
		f.b.lines = append(f.b.lines, renderLine{id: f.b.id, src: f.b.src, col: w.offset(line), text: string(body) + grid.join(w, line)})
	}
	return f.b.src + 1
}

// wrapRow wraps every cell to its column; a missing cell is one empty line.
func (l tableLayout) wrapRow(row []cell) wrappedRow {
	w := wrappedRow{row: row, lines: make([][]string, len(l.widths)), offsets: make([][]int, len(l.widths))}
	for k := range l.widths {
		w.lines[k] = []string{""}
		if k < len(row) {
			w.lines[k], w.offsets[k] = wrapCell(row[k].inline, l.widths[k], l.base)
		}
	}
	return w
}

func (w wrappedRow) height() int {
	height := 1
	for _, lines := range w.lines {
		height = max(height, len(lines))
	}
	return height
}

// offset is the source offset of the leftmost cell with text on a visual
// row; past every cell's text it keeps counting so the order stays increasing.
func (w wrappedRow) offset(line int) int {
	for k, c := range w.row {
		if line < len(w.offsets[k]) {
			return c.start + c.origin(w.offsets[k][line])
		}
	}
	if len(w.row) == 0 {
		return line
	}
	last := w.row[len(w.row)-1]
	return last.start + len(last.plain) + line
}

// join is one visual row: every column's line at that index, padded.
func (l tableLayout) join(w wrappedRow, line int) string {
	out := make([]string, len(l.widths))
	for k, width := range l.widths {
		out[k] = strings.Repeat(" ", width)
		if line < len(w.lines[k]) {
			out[k] = w.lines[k][line]
		}
	}
	return strings.Join(out, ruleStyle.Render(" │ "))
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
		out = append(out, c.render(segment{pos, pos + len(part)}, base)+strings.Repeat(" ", max(0, width-ansi.StringWidth(part))))
		offsets = append(offsets, pos)
		pos += len(part)
	}
	return out, offsets
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
