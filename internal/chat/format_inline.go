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
	var in inline
	var plain strings.Builder
	parse := len(src) <= inlineLimit
	i := 0
	for i < len(src) {
		if m, end := openingMarker(src, i); parse && m != nil {
			start := plain.Len()
			appendSafe(&in, &plain, src[i+len(m.open):end], i+len(m.open))
			in.spans = append(in.spans, span{start: start, end: plain.Len(), kind: m.kind})
			i = end + len(m.close)
			continue
		}
		size := runeLen(src, i)
		appendSafe(&in, &plain, src[i:i+size], i)
		i += size
	}
	in.plain = plain.String()
	in.orig = append(in.orig, len(src))
	return in
}

// openingMarker reports a marker opening at i with a balanced close, and the
// offset of that close.
func openingMarker(src string, i int) (*marker, int) {
	for k := range markers {
		m := &markers[k]
		if !strings.HasPrefix(src[i:], m.open) || (m.word && !boundaryBefore(src, i)) {
			continue
		}
		body := i + len(m.open)
		rel := strings.Index(src[body:], m.close)
		if rel <= 0 || (m.word && !boundaryAfter(src, body+rel+len(m.close))) {
			continue
		}
		if m.kind != spanCode && strings.TrimSpace(src[body:body+rel]) == "" {
			continue
		}
		return m, body + rel
	}
	return nil, 0
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

// appendSafe adds src text (already positioned at offset in the source line)
// to the plain text, escaping controls, and records each plain byte's origin.
// Joined emoji keep their joiner and variation selector, which safe() would
// escape for identities.
func appendSafe(in *inline, plain *strings.Builder, text string, offset int) {
	for j, r := range text {
		escaped := safe(string(r))
		if r == '\u200d' || r == '\ufe0f' {
			escaped = string(r)
		}
		for range len(escaped) {
			in.orig = append(in.orig, offset+j)
		}
		plain.WriteString(escaped)
	}
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
