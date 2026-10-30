package chat

import (
	"slices"
	"strings"
)

// /pair share in a ⇄ tab offers this workspace's own registered agents,
// bare or as @mentions; picking one only fills it in. An agent is named as
// typed, less one leading @, so @@x shares the agent called @x.

// shareCompletions are the local agents not yet on the line, for the ⇄
// tab's workspace: offered for the first agent, and for another once its
// first letters or @ are typed, so Enter after a pick shares.
func (m *Model) shareCompletions(l argLine) []string {
	if !m.conv.selected.isContact() || afterPick(l) {
		return nil
	}
	if strings.HasPrefix(l.current, "@") {
		return m.mentionCompletions(l, m.localAgents)
	}
	typed := l.typed()
	for _, w := range l.words[1:] {
		typed[w] = true
	}
	var out []string
	for _, id := range m.localAgents(l.current) {
		if !typed[id] {
			out = append(out, id)
		}
	}
	return out
}

// afterPick: an agent is on the line and nothing is typed after it.
func afterPick(l argLine) bool {
	return len(l.words) > 1 && l.current == ""
}

// localAgents are this workspace's registered agents starting with
// fragment, sorted; never a paired workspace's.
func (m *Model) localAgents(fragment string) []string {
	var ids []string
	for _, a := range m.agents.list {
		if strings.HasPrefix(a.AgentID, fragment) {
			ids = append(ids, a.AgentID)
		}
	}
	slices.Sort(ids)
	return ids
}

// shareLabel is a picker row: the local agent, and the workspace it would
// be shared with.
func (m *Model) shareLabel(item string) (string, bool) {
	key := m.conv.selected
	if !key.isContact() {
		return "", false
	}
	return "  " + safe(item) + "  " + mutedStyle.Render("local agent · share with ⇄ "+safe(m.contactTitle(key))), true
}

// sharedAgents are the agents a share or unshare names: each as typed,
// less one leading @; * stays as it is.
func sharedAgents(words []string) []string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = strings.TrimPrefix(w, "@")
	}
	return out
}
