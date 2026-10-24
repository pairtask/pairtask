package chat

import (
	"strings"
	"unicode"

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
	if p.parse {
		if m, end := openingMarker(p.src, i); m != nil {
			start := p.plain.Len()
			p.append(i+len(m.open), end)
			p.in.spans = append(p.in.spans, span{start: start, end: p.plain.Len(), kind: m.kind})
			return end + len(m.close)
		}
	}
	size := runeLen(p.src, i)
	p.append(i, i+size)
	return i + size
}

// append adds src[from:to] to the plain text, escaping controls, and records
// each plain byte's source offset. Joined emoji keep their joiner and
// variation selector, which safe() would escape for identities.
func (p *inlineParser) append(from, to int) {
	for j, r := range p.src[from:to] {
		escaped := safe(string(r))
		if r == '\u200d' || r == '\ufe0f' {
			escaped = string(r)
		}
		for range len(escaped) {
			p.in.orig = append(p.in.orig, from+j)
		}
		p.plain.WriteString(escaped)
	}
}

// openingMarker reports a marker opening at i with a balanced close, and the
// offset of that close.
func openingMarker(src string, i int) (*marker, int) {
	for k := range markers {
		if end := markers[k].closes(src, i); end > 0 {
			return &markers[k], end
		}
	}
	return nil, 0
}

// closes is the offset of the balanced close for a marker opening at i, or
// 0 when the marker doesn't open there or doesn't close.
func (m *marker) closes(src string, i int) int {
	if !m.opensAt(src, i) {
		return 0
	}
	body := i + len(m.open)
	rel := strings.Index(src[body:], m.close)
	if rel <= 0 || !m.endsAt(src, body+rel) {
		return 0
	}
	if m.kind != spanCode && strings.TrimSpace(src[body:body+rel]) == "" {
		return 0
	}
	return body + rel
}

func (m *marker) opensAt(src string, i int) bool {
	return strings.HasPrefix(src[i:], m.open) && (!m.word || boundaryBefore(src, i))
}

func (m *marker) endsAt(src string, close int) bool {
	return !m.word || boundaryAfter(src, close+len(m.close))
}

func boundaryBefore(src string, i int) bool {
	return i == 0 || !isWord(lastRune(src[:i]))
}

func boundaryAfter(src string, i int) bool {
	return i >= len(src) || !isWord(firstRune(src[i:]))
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

// render styles plain[from:to], applying base everywhere and the spans on top.
func (in inline) render(from, to int, base lipgloss.Style) string {
	var out strings.Builder
	at := from
	for at < to {
		next, style := in.styleAt(at, to, base)
		out.WriteString(style.Render(in.plain[at:next]))
		at = next
	}
	return out.String()
}

// styleAt is the style of the plain text at pos and where it changes next.
func (in inline) styleAt(pos, limit int, base lipgloss.Style) (int, lipgloss.Style) {
	next, style := limit, base
	for _, s := range in.spans {
		switch {
		case s.start <= pos && pos < s.end:
			style = spanStyle(s.kind, style)
			next = min(next, s.end)
		case s.start > pos:
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

func runeLen(s string, i int) int {
	_, size := firstRuneSize(s[i:])
	return size
}

func firstRuneSize(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 1
}

func firstRune(s string) rune {
	r, _ := firstRuneSize(s)
	return r
}

func lastRune(s string) rune {
	var last rune
	for _, r := range s {
		last = r
	}
	return last
}
