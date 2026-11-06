package chat

import (
	"sort"
	"strings"
)

// mentionFragment is the "@…" token being typed at the end of the leading
// prefix, without the "@".
func (m *Model) mentionFragment() (string, bool) {
	c := &m.compose
	if c.pasted || c.editor.Line() != 0 {
		return "", false
	}
	return lastMentionToken(c.editor.Value())
}

func lastMentionToken(value string) (string, bool) {
	fields := strings.Fields(value)
	if len(fields) == 0 || !openToken(value) {
		return "", false // also Unicode-only whitespace, which Fields drops
	}
	if !leadingMentions(fields) {
		return "", false // the prefix already ended
	}
	return strings.TrimPrefix(fields[len(fields)-1], "@"), true
}

// openToken reports a single-line, unescaped draft whose cursor (at the end)
// is still inside a word.
func openToken(value string) bool {
	if value == "" || strings.Contains(value, "\n") {
		return false
	}
	if strings.HasPrefix(strings.TrimLeft(value, " \t"), `\@`) {
		return false
	}
	last := value[len(value)-1]
	return last != ' ' && last != '\t'
}

// agentCandidates lists agent IDs matching a fragment: online first, then
// agents seen in the conversation. The @all keyword is typed, never picked,
// so an agent named "all" stays distinct from it.
func (m *Model) agentCandidates(fragment string) []string {
	return append(m.onlineIDs(fragment), m.conversationIDs(fragment)...)
}

func (m *Model) onlineIDs(fragment string) []string {
	var ids []string
	for _, a := range m.agents.list {
		ids = append(ids, a.AgentID)
	}
	return pickable(ids, fragment, nil)
}

// conversationIDs lists agents seen in the loaded conversation that aren't online.
func (m *Model) conversationIDs(fragment string) []string {
	online := map[string]bool{}
	for _, a := range m.agents.list {
		online[a.AgentID] = true
	}
	var ids []string
	for _, e := range m.timeline.order {
		ids = append(append(ids, e.msg.From, e.msg.To), e.msg.Group...)
	}
	return pickable(ids, fragment, online)
}

// pickable keeps agent IDs that start with fragment, once each, sorted,
// leaving out the broker's own names and anything in skip.
func pickable(ids []string, fragment string, skip map[string]bool) []string {
	seen := map[string]bool{"": true, "*": true, "operator": true}
	for id := range skip {
		seen[id] = true
	}
	var out []string
	for _, id := range ids {
		if seen[id] || !strings.HasPrefix(id, fragment) {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
