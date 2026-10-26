package chat

import (
	"slices"
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Mentions in a room or group (#164) are attention, not routing: the
// message still goes to the whole room or group, and the named members are
// marked as the ones it addresses. The prefix stays in the text, so a reader
// without the field still sees who was meant.

// attentionMentions is the selected room's or group's audience with the
// mentioned members marked.
func (m *Model) attentionMentions(tokens []string) audience {
	a := m.explicitAudience()
	if a.err != "" || a.source != SourceConversation {
		return audience{err: "remove the mentions, or send from the conversation itself"}
	}
	ids := slices.Clone(tokens)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if err := m.checkMentions(ids); err != "" {
		return audience{err: err}
	}
	a.mentions = ids
	return a
}

// checkMentions refuses what the broker would: @all, too many names, or a
// name outside a group or a fully known room; a partial room list leaves
// the room check to the broker.
func (m *Model) checkMentions(ids []string) string {
	switch {
	case slices.Contains(ids, "all"):
		return "@all can't be used in a room or group; it already reaches everyone in it"
	case len(ids) > protocol.MaxMentions:
		return "at most 16 mentions"
	case !m.session.HasCapability(protocol.CapabilityMentions):
		return "this broker can't carry mentions; upgrade it, or remove them"
	}
	members, complete := m.eligibleMembers()
	for _, id := range ids {
		if complete && !slices.Contains(members, id) {
			return safe(id) + " isn't in " + m.conv.selected.label()
		}
	}
	return ""
}

// eligibleMembers is who may be mentioned in the selected room or group, and
// whether that list is complete.
func (m *Model) eligibleMembers() ([]string, bool) {
	key := m.conv.selected
	if key.isGroup() {
		return key.group(), true
	}
	for _, r := range m.rooms {
		if r.ID == key.room {
			return r.Members, !m.roomsCut && !r.MembersTruncated
		}
	}
	return nil, false
}

// mentionCandidates are the @ picker's choices: in a room or group only its
// members (offline ones too) not yet named, elsewhere every known agent.
func (m *Model) mentionCandidates(fragment string) []string {
	key := m.conv.selected
	if !key.isGroup() && !key.isRoom() {
		return m.agentCandidates(fragment)
	}
	members, _ := m.eligibleMembers()
	typed := m.namedBefore()
	members = slices.DeleteFunc(slices.Clone(members), func(id string) bool { return slices.Contains(typed, id) })
	slices.Sort(members)
	return withPrefix(members, fragment)
}

// addressedNote is " · addressed @a @b" for mentioned members, or "".
func addressedNote(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	shown := make([]string, len(ids))
	for i, id := range ids {
		shown[i] = "@" + safe(id)
	}
	return " · addressed " + strings.Join(shown, " ")
}

// namedBefore are the mentions already complete in the draft, leaving out the
// one being typed.
func (m *Model) namedBefore() []string {
	value := m.compose.editor.Value()
	tokens := parsePrefix(value).tokens
	if len(tokens) > 0 && openToken(value) {
		tokens = tokens[:len(tokens)-1]
	}
	return tokens
}
