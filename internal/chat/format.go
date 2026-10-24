package chat

import (
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Message text is rendered with a small, fixed subset of Markdown: headings,
// list items with a hanging indent, pipe tables, fenced code, and inline
// `code`, **bold** / __bold__, *italic* / _italic_ and <u>underline</u>.
// Markers that don't balance within a line stay literal; nothing else is
// interpreted. Every source line keeps one logical line number (src), so
// resize anchors and /find offsets still point into the original text.

var (
	boldStyle      = lipgloss.NewStyle().Bold(true)
	italicStyle    = lipgloss.NewStyle().Italic(true)
	underlineStyle = lipgloss.NewStyle().Underline(true)
	codeStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	fenceStyle     = lipgloss.NewStyle().Faint(true)

	headingMark = regexp.MustCompile(`^#{1,6}\s+`)
	listMark    = regexp.MustCompile(`^(\s*)([-*•]|\d{1,3}[.)])\s+`)
	tableSep    = regexp.MustCompile(`^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?\s*$`)
)

// formatBody lays out a message's text as wrapped, styled lines.
func (b *lineBuilder) formatBody(text string, width int) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	f := formatter{b: b, width: width}
	for i := 0; i < len(lines); i++ {
		i = f.line(lines, i)
	}
}

type formatter struct {
	b      *lineBuilder
	width  int
	fenced bool // inside ``` … ```
	blank  bool // the previous rendered row was blank
}

// line renders lines[i] (and, for a table, the rows that follow it) and
// returns the index of the last line consumed.
func (f *formatter) line(lines []string, i int) int {
	line := lines[i]
	switch {
	case strings.HasPrefix(strings.TrimSpace(line), "```"):
		f.fenced = !f.fenced
		f.b.skip()
	case f.fenced:
		f.code(line)
	case strings.TrimSpace(line) == "":
		f.blankRow()
	case i+1 < len(lines) && isTableRow(line) && tableSep.MatchString(lines[i+1]):
		f.blank = false
		return f.table(lines, i)
	default:
		f.blank = false
		f.text(line)
	}
	return i
}

func (f *formatter) blankRow() {
	if f.blank {
		f.b.skip() // collapsed: the source line still counts
		return
	}
	f.blank = true
	f.b.add([]string{""}, body)
}

// code renders one fenced line verbatim, hard-wrapped so nothing is lost;
// offsets come from the plain parts, the style is applied after.
func (f *formatter) code(line string) {
	f.blank = false
	plain := safeText(line)
	parts := strings.Split(ansi.Hardwrap(plain, max(1, f.width-len(body)), true), "\n")
	pos := 0
	for _, part := range parts {
		if at := strings.Index(plain[pos:], part); at >= 0 {
			pos += at
		}
		f.b.lines = append(f.b.lines, renderLine{id: f.b.id, src: f.b.src, col: pos, text: string(body) + fenceStyle.Render(part)})
		pos += len(part)
	}
	f.b.src++
}

// text renders a heading, a list item or a paragraph line with inline styles.
func (f *formatter) text(line string) {
	if m := headingMark.FindString(line); m != "" {
		f.styledLine(line[len(m):], lead{skip: len(m)}, boldStyle)
		return
	}
	if m := listMark.FindString(line); m != "" {
		f.styledLine(line[len(m):], f.listLead(m), lipgloss.NewStyle())
		return
	}
	f.styledLine(line, lead{}, lipgloss.NewStyle())
}

// lead is what precedes a line's text: the marker shown on the first visual
// row, the hanging indent on the following rows, and how many source bytes
// the marker took.
type lead struct {
	first, rest string
	skip        int
}

// listLead keeps the marker visible, controls escaped, and bounds a deep
// indentation so the text keeps at least half the width.
func (f *formatter) listLead(marker string) lead {
	first := safeText(marker)
	limit := max(2, (f.width-len(body))/2)
	if w := ansi.StringWidth(first); w > limit {
		first = strings.TrimLeft(first, " ")
		if ansi.StringWidth(first) > limit {
			first = ansi.Truncate(first, limit, "")
		}
	}
	return lead{first: first, rest: strings.Repeat(" ", ansi.StringWidth(first)), skip: len(marker)}
}

// styledLine wraps the plain text of a line and prefixes the visual rows with
// the lead; col offsets point into the source line, marker included.
func (f *formatter) styledLine(text string, l lead, base lipgloss.Style) {
	in := inlineSpans(text)
	avail := max(1, f.width-len(body)-ansi.StringWidth(l.first))
	parts := wrap(in.plain, avail)
	pos := 0
	for i, part := range parts {
		if at := strings.Index(in.plain[pos:], part); at >= 0 {
			pos += at
		}
		prefix := l.rest
		if i == 0 {
			prefix = l.first
		}
		f.b.lines = append(f.b.lines, renderLine{id: f.b.id, src: f.b.src, col: l.skip + in.origin(pos), text: string(body) + prefix + in.render(pos, pos+len(part), base)})
		pos += len(part)
	}
	f.b.src++
}

// skip consumes a source line that renders nothing.
func (b *lineBuilder) skip() { b.src++ }
