package chat

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/pairtask/pairtask/internal/protocol"
)

// /ack offers what it can acknowledge: all, from, then each received
// message still needing it, newest first; after from, the agents, by name,
// with the tab each has. Picking only fills the line; the next Enter
// acknowledges. Acknowledging means received, never approval.

// ackCompletions are /ack's choices for the word being typed.
func (m *Model) ackCompletions(l argLine) []string {
	switch {
	case len(l.words) == 0:
		rows := m.ackRows()
		if len(rows) == 0 {
			return nil
		}
		return withPrefix(append([]string{"all", "from"}, rows...), l.current)
	case len(l.words) == 1 && l.words[0] == "from":
		return withPrefix(m.ackSenders(), l.current)
	}
	return nil
}

// ackRows are the received messages still needing acknowledgment, newest
// first, as #seq.
func (m *Model) ackRows() []string {
	var rows []string
	for i := len(m.timeline.order) - 1; i >= 0; i-- {
		if e := m.timeline.order[i]; m.targetable(e) && m.unacknowledged(e) {
			rows = append(rows, fmt.Sprintf("#%d", e.msg.Seq))
		}
	}
	return rows
}

// ackSenders are the agents /ack from can name: each a single word.
func (m *Model) ackSenders() []string {
	var ids []string
	for _, id := range m.agentCandidates("") {
		if oneWordAgent(id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// oneWordAgent: a valid agent ID that a command line can carry as one word.
func oneWordAgent(id string) bool {
	if strings.ContainsFunc(id, unicode.IsSpace) {
		return false
	}
	return protocol.ValidateAgentID(id) == nil
}

// ackPickLabel is an /ack picker row.
func (m *Model) ackPickLabel(item string) (string, bool) {
	l, ok := m.argumentLine()
	if !ok || l.name != "ack" {
		return "", false
	}
	if len(l.words) == 1 && l.words[0] == "from" {
		return m.senderLabel(item), true
	}
	switch item {
	case "all":
		return "  all   " + mutedStyle.Render("every received message, in all conversations (received, not approval)"), true
	case "from":
		return "  from  " + mutedStyle.Render("every message from one agent"), true
	}
	if e := m.entryAt(item); e != nil {
		return m.ackLabel(e.msg.MessageID), true
	}
	return "", false
}

// senderLabel is an agent row: its name, and its tab when it has one.
func (m *Model) senderLabel(id string) string {
	if n := m.tabOf(id); n > 0 {
		return "  " + safe(id) + "  " + mutedStyle.Render(fmt.Sprintf("tab %d", n))
	}
	return "  " + safe(id)
}

// entryAt is the loaded received entry a "#seq" names.
func (m *Model) entryAt(token string) *entry {
	seq, err := strconv.ParseUint(strings.TrimPrefix(token, "#"), 10, 64)
	if err != nil || !strings.HasPrefix(token, "#") {
		return nil
	}
	for _, e := range m.timeline.order {
		if e.msg.Seq == seq && m.targetable(e) {
			return e
		}
	}
	return nil
}

// tabOf is the conversation bar position of an agent's 1:1 tab, 0 without
// one; tabAt is the agent whose 1:1 tab is at a position.
func (m *Model) tabOf(id string) int {
	for n, key := range m.tabs() {
		if key == agentKey(id) {
			return n
		}
	}
	return 0
}

func (m *Model) tabAt(n int) string {
	return m.tabs()[n].agentID
}

// tabs are the visible conversations by bar position, All being 1.
func (m *Model) tabs() map[int]viewKey {
	out := map[int]viewKey{}
	position := 1
	for _, key := range m.conv.order {
		if !m.conv.hidden[key] {
			position++
			out[position] = key
		}
	}
	return out
}

// fromHint explains a numeric /ack from that names no sender: it takes an
// agent's name, not a tab or a message number. An agent really called so
// is acknowledged as any other.
func (m *Model) fromHint(from string, usage protocol.InboxStatus) string {
	n, err := strconv.Atoi(strings.TrimPrefix(from, "#"))
	if err != nil || n <= 0 || slices.ContainsFunc(usage.Senders, func(s protocol.SenderUsage) bool { return s.AgentID == from }) {
		return ""
	}
	hint := fmt.Sprintf("no messages from %q: /ack from takes an agent's name", from)
	if agent := m.tabAt(n); agent != "" {
		hint += fmt.Sprintf("; tab %d is %s: /ack from %s", n, safe(agent), safe(agent))
	}
	return hint + fmt.Sprintf("; message #%d: /ack #%d", n, n)
}
