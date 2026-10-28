package chat

import (
	"slices"

	tea "charm.land/bubbletea/v2"
)

// The command line runs only what the operator typed (#208): every rune of
// the line remembers whether it was typed or pasted, so a pasted argument
// after a typed "/review " runs, while a command name that was pasted, even
// one character of it and wherever the cursor stood, never does.

// update applies a key or a paste to the input and records which runes it
// brought in.
func (l *commandLine) update(msg tea.Msg, pasted bool) tea.Cmd {
	before := l.input.Value()
	var cmd tea.Cmd
	l.input, cmd = l.input.Update(msg)
	l.track(before, pasted)
	return cmd
}

// set replaces the line with a value the operator chose (a completion, or
// their own command given back), as typed text.
func (l *commandLine) set(value string) {
	l.input.SetValue(value)
	l.input.CursorEnd()
	l.pasted = make([]bool, len([]rune(l.input.Value())))
}

// track marks the one region an edit changed: the runes before it and after
// it keep their marks, the runes it inserted take the edit's.
func (l *commandLine) track(before string, pasted bool) {
	old, now := []rune(before), []rune(l.input.Value())
	if len(l.pasted) != len(old) { // never out of step: unknown text is pasted
		l.pasted = filled(len(old), true)
	}
	head := 0
	for head < len(old) && head < len(now) && old[head] == now[head] {
		head++
	}
	tail := 0
	for tail < len(old)-head && tail < len(now)-head && old[len(old)-1-tail] == now[len(now)-1-tail] {
		tail++
	}
	marks := make([]bool, 0, len(now))
	marks = append(marks, l.pasted[:head]...)
	marks = append(marks, filled(len(now)-head-tail, pasted)...)
	l.pasted = append(marks, l.pasted[len(old)-tail:]...)
}

func filled(n int, v bool) []bool {
	out := make([]bool, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// trusted reports a command name typed in full: every rune up to and
// including the space after the name, or the whole line when it is only a
// name, was typed.
func (l *commandLine) trusted() bool {
	end := min(nameEnd([]rune(l.input.Value())), len(l.pasted))
	return !slices.Contains(l.pasted[:end], true)
}

// nameEnd is where the command name ends, its space included: the first
// space after a non-space, or the end of the line.
func nameEnd(value []rune) int {
	for i := 1; i < len(value); i++ {
		if value[i] == ' ' && value[i-1] != ' ' {
			return i + 1
		}
	}
	return len(value)
}
