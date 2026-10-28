package chat

import (
	"slices"
	"strings"
)

// guardSend holds a draft that reads as a command (#208): a recalled or
// pasted "/review …" sent as a message looks like nothing happened. The
// first Enter only says so; Enter again on the unchanged draft sends it as
// the message it is, and any edit arms the guard again. A draft begun with
// "//" asked for its literal slash and is sent at once.
func (m *Model) guardSend() bool {
	c := &m.compose
	if c.slash {
		return false // "//" asked for a literal slash
	}
	text := m.outgoingText()
	name, ok := commandLike(text)
	if !ok || c.guard == text {
		return false
	}
	c.guard = text
	m.status = "this looks like the /" + name + " command, not a message: Enter again sends it as a message; to run it, clear the draft and type /" + name
	return true
}

// commandLike reports a text starting with "/NAME" for a command's exact
// name or alias, followed by a space, a line break or nothing.
func commandLike(text string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimLeft(text, " \t\n"), "/")
	if !ok {
		return "", false
	}
	word := rest
	if i := strings.IndexAny(rest, " \t\n"); i >= 0 {
		word = rest[:i]
	}
	for _, c := range commands {
		if c.name == word || slices.Contains(c.aliases, word) {
			return c.name, true
		}
	}
	return "", false
}
