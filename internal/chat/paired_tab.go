package chat

import (
	"slices"
	"strings"
	"unicode"

	"github.com/pairtask/pairtask/internal/protocol"
)

// A ⇄ tab's addressing (#327): the @ picker offers both sides at once,
// theirs then ours, each row a tag as it is written; the header resolves
// only finished tags and says so while one is being written; a sent row
// names who it went to here as the broker stored it.

// pickQuery is what a ⇄ tab's @ picker is asked: the tab's contact and the
// word typed after the @.
type pickQuery struct{ contact, fragment string }

// contactCandidates are a ⇄ tab's @ picker choices: the agents that
// workspace shares, then, where the broker takes tags here, their tags
// qualified and their operator, then ours as AGENT@here and our operator.
func (m *Model) contactCandidates(q pickQuery) []string {
	theirs := m.unnamed(m.sharedByContact(q.contact), q.fragment)
	if !m.offersBothSides(q) {
		return theirs
	}
	theirs = slices.DeleteFunc(theirs, func(id string) bool { return id == protocol.OperatorID }) // bare @operator is ours
	theirs = append(theirs, m.untyped(m.theirTags(q), q)...)
	return append(theirs, m.untyped(m.homeTags(), q)...)
}

// offersBothSides: the picker offers ours too, once something is typed or
// from a bare @ when an agent here can take a tag.
func (m *Model) offersBothSides(q pickQuery) bool {
	if !m.pairedMentions() {
		return false
	}
	return q.fragment != "" || slices.ContainsFunc(m.agents.list, taggable)
}

// taggable: the agent can take an operator message, as a tag here makes.
func taggable(a protocol.AgentPresence) bool { return a.ProtocolVersion >= protocol.OriginVersion }

// theirTags are their agents qualified, once the typing names a scope, and
// their operator: AGENT@WS, operator@WS. A workspace this side calls "here",
// or by no valid name, is named by its key.
func (m *Model) theirTags(q pickQuery) []string {
	qualifier := m.contactTitle(contactKey(q.contact))
	if qualifier == hereScope || !mentionID.MatchString(qualifier) {
		qualifier = q.contact
	}
	var tags []string
	if strings.Contains(q.fragment, "@") {
		for _, id := range m.sharedByContact(q.contact) {
			tags = append(tags, id+"@"+qualifier)
		}
	}
	slices.Sort(tags)
	return append(tags, protocol.OperatorID+"@"+qualifier)
}

// homeTags are ours: each agent here as AGENT@here, then our operator.
func (m *Model) homeTags() []string {
	var tags []string
	for _, id := range m.localAgents("") {
		tags = append(tags, id+"@"+hereScope)
	}
	return append(tags, protocol.OperatorID)
}

// untyped are the tags starting with fragment that the line doesn't
// already name.
func (m *Model) untyped(tags []string, q pickQuery) []string {
	typed := m.namedBefore()
	return withPrefix(slices.DeleteFunc(tags, func(tag string) bool { return slices.Contains(typed, tag) }), q.fragment)
}

// agentPickNote follows an @ picker row: the workspace a bare agent of
// theirs belongs to, or which side a tag names; a tag already names its
// scope, so nothing is appended to it.
func (m *Model) agentPickNote(item string) string {
	if note := m.sideNote(parseTag(item)); note != "" {
		return mutedStyle.Render("  " + note)
	}
	if strings.Contains(item, "@") {
		return ""
	}
	return m.pickedWorkspace()
}

// sideNote names an operator's or an agent here's side in a ⇄ tab, or "".
func (m *Model) sideNote(t tagWord) string {
	if !m.conv.selected.isContact() || !m.pairedMentions() {
		return ""
	}
	switch {
	case t.word == protocol.OperatorID:
		return "your operator"
	case t.scope == hereScope:
		return "your agent"
	case t.qualified() && t.operator():
		return "their operator"
	}
	return ""
}

// editingTag is the leading @word the cursor is in, while it's unfinished:
// neither followed by a space nor picked yet.
func (m *Model) editingTag() (string, bool) {
	e := &m.compose.editor
	if !m.pairedMentions() || e.Line() != 0 {
		return "", false
	}
	line, _, _ := strings.Cut(e.Value(), "\n")
	runes := []rune(line)
	return tagAtCursor(runes, min(e.Column(), len(runes)))
}

// tagAtCursor is the word the cursor at col ends or is inside, when it is
// one of the line's leading @words.
func tagAtCursor(runes []rune, col int) (string, bool) {
	if col == 0 || unicode.IsSpace(runes[col-1]) {
		return "", false
	}
	words := strings.Fields(string(runes[:col]))
	if !leadingMentions(words) {
		return "", false
	}
	end := col
	for end < len(runes) && !unicode.IsSpace(runes[end]) {
		end++
	}
	return words[len(words)-1] + string(runes[col:end]), true
}

// leadingMentions: every word is an @word, so the prefix hasn't ended.
func leadingMentions(words []string) bool {
	for _, w := range words {
		if !strings.HasPrefix(w, "@") {
			return false
		}
	}
	return true
}

// editingSegments say the addressing is still being written: who gets the
// draft shows once the tag is finished.
func editingSegments(word string) []hintSegment {
	return []hintSegment{{"✎ editing " + safe(word) + " · a space or a pick finishes it, then who gets it shows", mutedStyle}}
}

// storedHome is who here a sent message also went to, as the broker
// stored it, after its recipients there; "" when it tagged no one here.
func storedHome(v protocol.ContactMessageView) string {
	home := address{here: v.Here, operatorHere: v.OperatorHere}
	if v.Direction == "in" || !home.homeTagged() {
		return ""
	}
	return " · " + homeScope(home)
}
