package chat

import (
	"slices"
	"strings"
)

// PickerArgument completes a command's argument: room names and the words
// of /room, and agent mentions in /room add|remove and /chat (#164).
const PickerArgument = "argument"

// argLine is a command line split for completion: the command name, the
// complete words before the cursor, and the word being typed (possibly
// empty after a space).
type argLine struct {
	name    string
	words   []string
	current string
}

func splitArgLine(text string) (argLine, bool) {
	name, rest, hasArgs := strings.Cut(text, " ")
	if !hasArgs {
		return argLine{}, false
	}
	cut := strings.LastIndex(rest, " ") + 1
	return argLine{name: name, words: strings.Fields(rest[:cut]), current: rest[cut:]}, true
}

// typed are the agent IDs already on the line, so they aren't offered twice.
func (l argLine) typed() map[string]bool {
	out := map[string]bool{}
	for _, w := range l.words {
		if id, ok := strings.CutPrefix(w, "@"); ok {
			out[id] = true
		}
	}
	return out
}

// argumentCandidates lists the completions for the word being typed.
func (m *Model) argumentCandidates(l argLine) []string {
	switch l.name {
	case "room", "rooms":
		return m.roomCompletions(l)
	case "chat":
		return m.mentionCompletions(l, m.agentCandidates)
	}
	return nil
}

func (m *Model) roomCompletions(l argLine) []string {
	switch {
	case len(l.words) == 0:
		return withPrefix(append([]string{"add", "remove"}, m.roomNames()...), l.current)
	case l.words[0] != "add" && l.words[0] != "remove":
		return nil // /room NAME takes nothing more
	case len(l.words) == 1:
		return withPrefix(m.roomNames(), l.current)
	}
	room := l.words[1]
	if l.words[0] == "remove" {
		return m.mentionCompletions(l, func(frag string) []string { return withPrefix(m.sortedMembers(room), frag) })
	}
	return m.mentionCompletions(l, func(frag string) []string {
		return slices.DeleteFunc(m.agentCandidates(frag), m.memberOf(room))
	})
}

// mentionCompletions offers "@ID" for an "@fragment" being typed, leaving
// out IDs already on the line and IDs a mention can't carry.
func (m *Model) mentionCompletions(l argLine, source func(string) []string) []string {
	frag, ok := strings.CutPrefix(l.current, "@")
	if !ok {
		return nil
	}
	typed := l.typed()
	var out []string
	for _, id := range source(frag) {
		if !typed[id] && mentionID.MatchString(id) {
			out = append(out, "@"+id)
		}
	}
	return out
}

func (m *Model) roomNames() []string {
	names := make([]string, 0, len(m.rooms))
	for _, r := range m.rooms {
		names = append(names, r.ID)
	}
	slices.Sort(names)
	return names
}

func (m *Model) sortedMembers(room string) []string {
	members := slices.Clone(m.roomMembers(room))
	slices.Sort(members)
	return members
}

// memberOf reports, for use with DeleteFunc, whether an ID is in the room.
func (m *Model) memberOf(room string) func(string) bool {
	members := m.roomMembers(room)
	return func(id string) bool { return slices.Contains(members, id) }
}

// withPrefix keeps the items that start with prefix, in order.
func withPrefix(items []string, prefix string) []string {
	var out []string
	for _, it := range items {
		if strings.HasPrefix(it, prefix) {
			out = append(out, it)
		}
	}
	return out
}

// refreshArgumentPicker opens the argument picker for the word being typed,
// or closes it when there is nothing to offer. It stays shut while the
// cursor is inside the line, so a completion only ever replaces the word at
// the cursor, and after Esc until the text changes.
func (m *Model) refreshArgumentPicker(l argLine) {
	c := &m.compose
	if !m.argumentPickable() {
		c.picker = nil
		return
	}
	items := m.argumentCandidates(l)
	if len(items) == 0 {
		c.picker = nil
		return
	}
	c.picker = &picker{kind: PickerArgument, items: items, selected: c.keptSelection(items)}
}

// keptSelection keeps the highlighted row while the candidates are
// unchanged, and starts at the top otherwise.
func (c *composer) keptSelection(items []string) int {
	p := c.openPicker(PickerArgument)
	if p == nil || !slices.Equal(p.items, items) {
		return 0
	}
	return p.selected
}

func (m *Model) argumentPickable() bool {
	line := m.compose.cmd
	value := line.input.Value()
	if line.pasted || line.input.Position() != len([]rune(value)) {
		return false
	}
	return line.dismissed == nil || *line.dismissed != value
}

// completingArgument is an open argument picker whose highlighted
// candidate isn't the word already typed: Enter then completes it, and a
// word typed out in full and still highlighted runs the line as before.
func (m *Model) completingArgument() bool {
	p := m.compose.openPicker(PickerArgument)
	if p == nil {
		return false
	}
	value := m.compose.cmd.input.Value()
	return p.items[p.selected] != value[strings.LastIndex(value, " ")+1:]
}

// completeArgument replaces the word being typed with the selected
// candidate and a space; it never runs the command.
func (m *Model) completeArgument() {
	c := &m.compose
	p := c.openPicker(PickerArgument)
	if p == nil || c.cmd.pasted {
		return
	}
	value := c.cmd.input.Value()
	cut := strings.LastIndex(value, " ") + 1
	c.cmd.input.SetValue(value[:cut] + p.items[p.selected] + " ")
	c.cmd.input.CursorEnd()
	c.picker = nil
	m.refreshCommandPicker() // the next word may have candidates of its own
}
