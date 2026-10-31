package chat

import (
	"strings"
)

// Enter never runs a command line still lacking the word it needs, nor
// drops it: it offers that word's choices in the picker, or, with none,
// keeps the line and says what is missing. A complete line runs.

// bareMenus are commands that run nothing bare: Enter offers their first
// word instead.
var bareMenus = map[string]bool{"ack": true, "room": true, "review": true, "chat": true}

// nothingToChoose says, for a command with a reason of its own, why there
// is nothing to pick.
var nothingToChoose = map[string]string{"ack": "nothing to acknowledge"}

// offersWord handles Enter on a line lacking its word; false lets the line
// run.
func (m *Model) offersWord(c command, arg argument) bool {
	if !lacksWord(c, arg) {
		return false
	}
	line := m.compose.cmd
	value := line.input.Value()
	l, _ := splitArgLine(strings.TrimRight(value, " ") + " ")
	if len(m.argumentCandidates(l)) == 0 {
		m.status = missingWord(c, arg)
		return true
	}
	if !strings.HasSuffix(value, " ") {
		line.input.SetValue(value + " ")
		line.input.CursorEnd()
		line.track(value, false)
	}
	line.dismissed = nil
	m.refreshCommandPicker()
	return true
}

// lacksWord: the command refuses the line as it is, or runs nothing bare.
// A secret command whose secret is on the line runs, to be refused and
// cleared as before.
func lacksWord(c command, arg argument) bool {
	if c.secret && secretTyped(arg) {
		return false
	}
	if bareMenus[c.name] && arg.empty() {
		return true
	}
	return c.check(arg) != nil
}

// secretTyped: a redemption with its code on the line.
func secretTyped(arg argument) bool {
	fields := strings.Fields(string(arg))
	return len(fields) >= 3 && fields[0] == "redeem"
}

// missingWord says what the line lacks.
func missingWord(c command, arg argument) string {
	if reason, ok := nothingToChoose[c.name]; ok {
		return reason
	}
	if err := c.check(arg); err != nil {
		return "/" + c.name + " " + err.Error()
	}
	return "/" + c.name + " " + c.usage
}
