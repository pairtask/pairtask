package chat

import "strings"

// The words of a /review line (#297): split on spaces, a word in double
// quotes kept whole.

// reviewLine is a /review line as typed.
type reviewLine string

// fields split it into words, a word in double quotes kept whole without
// them. A quote never closed stays in its word; unclosedQuote refuses such
// a line first.
func (l reviewLine) fields() []string {
	var words []string
	for rest := l.trimmed(); rest != ""; {
		var word string
		word, rest = rest.next()
		words = append(words, word)
	}
	return words
}

// unclosedQuote: a word opens a quote it never closes.
func (l reviewLine) unclosedQuote() bool {
	for rest := l.trimmed(); rest != ""; _, rest = rest.next() {
		if rest[0] == '"' && !strings.Contains(string(rest[1:]), `"`) {
			return true
		}
	}
	return false
}

func (l reviewLine) trimmed() reviewLine { return reviewLine(strings.TrimSpace(string(l))) }

// next is the first word and the rest after it, without the spaces and
// tabs before the next word.
func (l reviewLine) next() (string, reviewLine) {
	s := string(l)
	if quoted, tail, ok := strings.Cut(s[1:], `"`); s[0] == '"' && ok {
		return quoted, reviewLine(strings.TrimLeft(tail, " \t"))
	}
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], reviewLine(strings.TrimLeft(s[i:], " \t"))
	}
	return s, ""
}
