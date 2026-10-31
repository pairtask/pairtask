package chat

import (
	"fmt"
	"strconv"
)

// Commands that name a message offer the loaded ones, newest first:
// /stages and /claims sent ones, /copy any (or ref, then an agent),
// /diagram those holding a diagram, then which one when there are several.

// messageRows are the current-epoch messages want keeps, newest first, as
// #seq.
func (m *Model) messageRows(want func(*entry) bool) []string {
	var rows []string
	for i := len(m.timeline.order) - 1; i >= 0; i-- {
		if e := m.timeline.order[i]; e.epoch == m.epoch && want(e) {
			rows = append(rows, fmt.Sprintf("#%d", e.msg.Seq))
		}
	}
	return rows
}

func sentEntry(e *entry) bool { return e.direction == "sent" }

// sentCompletions offer /stages' and /claims' message.
func (m *Model) sentCompletions(l argLine) []string {
	if len(l.words) > 0 {
		return nil
	}
	return withPrefix(m.messageRows(sentEntry), l.current)
}

// copyCompletions offer a message, or ref and then an agent.
func (m *Model) copyCompletions(l argLine) []string {
	switch {
	case len(l.words) == 0:
		return withPrefix(append([]string{"ref"}, m.messageRows(anyEntry)...), l.current)
	case len(l.words) == 1 && l.words[0] == "ref":
		return withPrefix(m.ackSenders(), l.current)
	}
	return nil
}

// diagramCompletions offer the messages holding a diagram, then, when one
// holds several, which.
func (m *Model) diagramCompletions(l argLine) []string {
	switch len(l.words) {
	case 0:
		return withPrefix(m.messageRows(func(e *entry) bool { return len(m.diagramsOf(e)) > 0 }), l.current)
	case 1:
		e := m.entryAt(l.words[0])
		if e == nil {
			return nil
		}
		return withPrefix(blockNumbers(len(m.diagramsOf(e))), l.current)
	}
	return nil
}

// diagramsOf are the diagram blocks of a message whose text is loaded.
func (m *Model) diagramsOf(e *entry) []diagram {
	if e.omitted {
		return nil
	}
	return messageText(payloadText(e.msg.Payload)).lines().diagrams()
}

// blockNumbers are 1..n when there is a choice to make, none for one.
func blockNumbers(n int) []string {
	if n < 2 {
		return nil
	}
	out := make([]string, n)
	for i := range out {
		out[i] = strconv.Itoa(i + 1)
	}
	return out
}
