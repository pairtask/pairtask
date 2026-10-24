package chat

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

// spanKind is one inline style.
type spanKind int

const (
	spanCode spanKind = iota
	spanBold
	spanItalic
	spanUnderline
)

// span is a styled byte range of the plain text.
type span struct {
	start, end int
	kind       spanKind
}

// inline is a line's plain text (markers removed, controls escaped) with its
// styled spans and a map from plain byte offsets back to the source line.
type inline struct {
	plain string
	spans []span
	orig  []int // orig[i] is the source offset of plain byte i; len(plain) entries plus one
}

// marker is an inline delimiter and how it closes.
type marker struct {
	open, close string
	kind        spanKind
	word        bool // only at word boundaries (the underscore forms)
}

var markers = []marker{
	{"`", "`", spanCode, false},
	{"**", "**", spanBold, false},
	{"__", "__", spanBold, true},
	{"<u>", "</u>", spanUnderline, false},
	{"*", "*", spanItalic, false},
	{"_", "_", spanItalic, true},
}

// inlineLimit bounds the inline parser: a longer line is shown as typed, so
// a line full of unclosed markers cannot cost quadratic scans.
const inlineLimit = 4096

// inlineSpans parses one source line. A marker that has no balancing close
// on the same line stays literal.
func inlineSpans(src string) inline {
	p := inlineParser{src: src, parse: len(src) <= inlineLimit}
	for i := 0; i < len(src); {
		i = p.step(i)
	}
	p.in.plain = p.plain.String()
	p.in.orig = append(p.in.orig, len(src))
	return p.in
}

// inlineParser walks a source line, building the plain text and its spans.
type inlineParser struct {
	src   string
	parse bool
	plain strings.Builder
	in    inline
}

// step consumes a span or one rune at i and returns the next offset.
func (p *inlineParser) step(i int) int {
	c := cursor{src: p.src, at: i}
	if p.parse {
		if m, end := c.openingMarker(); m != nil {
			start := p.plain.Len()
			p.append(segment{i + len(m.open), end})
			p.in.spans = append(p.in.spans, span{start: start, end: p.plain.Len(), kind: m.kind})
			return end + len(m.close)
		}
	}
	size := c.runeLen()
	p.append(segment{i, i + size})
	return i + size
}

// segment is a byte range [from, to) of a string.
type segment struct {
	from, to int
}

// append adds the source segment to the plain text, escaping controls, and
// records each plain byte's source offset. Joined emoji keep their joiner
// and variation selector, which safe() would escape for identities.
func (p *inlineParser) append(seg segment) {
	for j, r := range p.src[seg.from:seg.to] {
		escaped := safe(string(r))
		if r == '\u200d' || r == '\ufe0f' {
			escaped = string(r)
		}
		for range len(escaped) {
			p.in.orig = append(p.in.orig, seg.from+j)
		}
		p.plain.WriteString(escaped)
	}
}

// cursor is a position in a source line.
type cursor struct {
	src string
	at  int
}

// openingMarker reports a marker opening here with a balanced close, and the
// offset of that close.
func (c cursor) openingMarker() (*marker, int) {
	for k := range markers {
		if end := markers[k].closes(c); end > 0 {
			return &markers[k], end
		}
	}
	return nil, 0
}

// closes is the offset of the balanced close for a marker opening at the
// cursor, or 0 when it doesn't open there or doesn't close.
func (m *marker) closes(c cursor) int {
	if !m.opensAt(c) {
		return 0
	}
	body := c.at + len(m.open)
	rel := strings.Index(c.src[body:], m.close)
	if rel <= 0 || !m.endsAt(cursor{c.src, body + rel + len(m.close)}) {
		return 0
	}
	if m.kind != spanCode && strings.TrimSpace(c.src[body:body+rel]) == "" {
		return 0
	}
	return body + rel
}

func (m *marker) opensAt(c cursor) bool {
	return strings.HasPrefix(c.src[c.at:], m.open) && (!m.word || c.boundaryBefore())
}

func (m *marker) endsAt(c cursor) bool {
	return !m.word || c.boundaryAfter()
}

func (c cursor) boundaryBefore() bool {
	r, _ := utf8.DecodeLastRuneInString(c.src[:c.at])
	return c.at == 0 || !isWord(r)
}

func (c cursor) boundaryAfter() bool {
	r, _ := utf8.DecodeRuneInString(c.src[c.at:])
	return c.at >= len(c.src) || !isWord(r)
}

func (c cursor) runeLen() int {
	_, size := utf8.DecodeRuneInString(c.src[c.at:])
	return max(1, size)
}

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// plainText is a message's text as the operator reads it: markers removed,
// one line per source line.
func plainText(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, line := range lines {
		lines[i] = inlineSpans(line).plain
	}
	return strings.Join(lines, "\n")
}

// origin maps a plain byte offset to the source line.
func (in inline) origin(pos int) int {
	if pos >= len(in.orig) {
		return in.orig[len(in.orig)-1]
	}
	return in.orig[pos]
}

// render styles the plain segment, applying base everywhere and the spans
// on top.
func (in inline) render(seg segment, base lipgloss.Style) string {
	var out strings.Builder
	at := seg.from
	for at < seg.to {
		next, style := in.styleAt(segment{at, seg.to}, base)
		out.WriteString(style.Render(in.plain[at:next]))
		at = next
	}
	return out.String()
}

// styleAt is the style at the segment's start and where it changes next,
// bounded by the segment's end.
func (in inline) styleAt(seg segment, base lipgloss.Style) (int, lipgloss.Style) {
	next, style := seg.to, base
	for _, s := range in.spans {
		switch {
		case s.start <= seg.from && seg.from < s.end:
			style = spanStyle(s.kind, style)
			next = min(next, s.end)
		case s.start > seg.from:
			next = min(next, s.start)
		}
	}
	return next, style
}

func spanStyle(kind spanKind, base lipgloss.Style) lipgloss.Style {
	switch kind {
	case spanCode:
		return base.Foreground(codeStyle.GetForeground())
	case spanBold:
		return base.Bold(true)
	case spanItalic:
		return base.Italic(true)
	}
	return base.Underline(true)
}
