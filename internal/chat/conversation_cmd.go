package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// chatCommand: /chat all, /chat <id> for the mention grammar, /chat "id" (a
// JSON string) for any other ID, including an agent literally named all.
func (m *Model) chatCommand(arg argument) tea.Cmd {
	key, err := chatTarget(string(arg))
	if err != nil {
		m.status = err.Error()
		return nil
	}
	m.refreshDirectory()
	m.selectView(key)
	return nil
}

// chatTarget parses /chat: all, one agent (a mention, or a JSON string for
// any ID), or a group (two or more mentions, or a JSON array).
func chatTarget(arg string) (viewKey, error) {
	switch {
	case arg == ConversationAll:
		return allKey, nil
	case strings.HasPrefix(arg, `"`):
		var id string
		if err := json.Unmarshal([]byte(arg), &id); err != nil {
			return allKey, fmt.Errorf("/chat takes all, an agent mention, or a JSON string: %w", err)
		}
		return agentKey(id), protocol.ValidateAgentID(id)
	case strings.HasPrefix(arg, "["):
		var ids []string
		if err := json.Unmarshal([]byte(arg), &ids); err != nil {
			return allKey, fmt.Errorf("/chat takes a JSON array of agent IDs for a group: %w", err)
		}
		return membersKey(ids)
	}
	fields := strings.Fields(arg)
	ids := make([]string, 0, len(fields))
	for _, f := range fields {
		id := strings.TrimPrefix(f, "@")
		if !mentionID.MatchString(id) {
			return allKey, fmt.Errorf("/chat takes all, agent mentions, a JSON string or a JSON array")
		}
		ids = append(ids, id)
	}
	return membersKey(ids)
}

// membersKey is the conversation of the given agents: a 1:1 for one, a
// group for two or more.
func membersKey(ids []string) (viewKey, error) {
	members := canonicalGroup(ids)
	for _, id := range members {
		if err := protocol.ValidateAgentID(id); err != nil {
			return allKey, err
		}
	}
	switch len(members) {
	case 0:
		return allKey, fmt.Errorf("/chat needs at least one agent")
	case 1:
		return agentKey(members[0]), nil
	}
	if len(members) > protocol.MaxRecipients {
		return allKey, fmt.Errorf("a group has at most %d agents", protocol.MaxRecipients)
	}
	return groupKey(members), nil
}

// closeCommand hides the selected conversation from the bar and returns to
// All. It is UI only: no message, receipt, acknowledgment or recipient
// changes, and new traffic or /chat shows it again.
func (m *Model) closeCommand(argument) tea.Cmd {
	key := m.conv.selected
	if key.common() {
		m.status = "All can't be closed"
		return nil
	}
	m.selectView(allKey)
	m.conv.hidden[key] = true
	m.status = key.label() + " closed; new messages or /chat reopen it"
	return nil
}

// forgetCommand is /forget: the selected group or 1:1 leaves the bar and
// stays out as traffic arrives, until /chat reopens it. Its messages stay
// in All, and an unsent draft stays with it.
func (m *Model) forgetCommand(argument) tea.Cmd {
	key := m.conv.selected
	switch {
	case key.common():
		m.status = "All can't be forgotten"
		return nil
	case key.isRoom():
		m.status = "use /close, or /room delete " + key.room
		return nil
	}
	m.selectView(allKey)
	m.conv.hidden[key], m.conv.forgot[key] = true, true
	m.status = "forgot " + key.label() + "; its messages stay in All; " + reopenHint(key) + " reopens it" + m.draftNote(key)
	return nil
}

// reopenHint is the /chat command that reopens a conversation.
func reopenHint(key viewKey) string {
	if key.isGroup() {
		return "/chat @" + strings.Join(key.group(), " @")
	}
	return "/chat @" + key.agent()
}

// reopenFor shows hidden conversations an entry belongs to; forgotten ones
// stay out until /chat.
func (m *Model) reopenFor(e *entry) {
	for _, key := range entryViews(e) {
		m.reopen(key)
	}
}

func (m *Model) reopen(key viewKey) {
	if !m.conv.forgot[key] {
		delete(m.conv.hidden, key)
	}
}

// reopenScope shows the hidden conversation of a send's recipients.
func (m *Model) reopenScope(scope protocol.Message) {
	switch {
	case len(scope.Group) > 1:
		m.reopen(groupKey(scope.Group))
	case len(scope.Group) == 1:
		m.reopen(agentKey(scope.Group[0]))
	case scope.To != "" && scope.To != protocol.Broadcast:
		m.reopen(agentKey(scope.To))
	}
}

func chatArg(a argument) error {
	if a.empty() {
		return fmt.Errorf("takes all, an agent mention, or a JSON string")
	}
	_, err := chatTarget(string(a))
	return err
}
