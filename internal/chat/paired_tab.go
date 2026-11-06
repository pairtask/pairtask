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
	tags := append(append(theirs, m.theirTags(q)...), m.homeTags()...)
	return m.untyped(tags, q)
}

// offersBothSides: on a broker that takes tags here, the picker offers
// ours and both operators from the first @, whoever is online (#327).
func (m *Model) offersBothSides(q pickQuery) bool { return m.pairedMentions() }

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
		for _, id := range taggable(m.sharedByContact(q.contact)) {
			tags = append(tags, id+"@"+qualifier)
		}
	}
	slices.Sort(tags)
	return append(tags, protocol.OperatorID+"@"+qualifier)
}

// homeTags are ours: each agent here as AGENT@here, then our operator.
func (m *Model) homeTags() []string {
	var tags []string
	for _, id := range taggable(m.localAgents("")) {
		tags = append(tags, id+"@"+hereScope)
	}
	return append(tags, protocol.OperatorID)
}

// taggable are the IDs a tag can carry: the mention grammar's, without an
// @ or space of their own.
func taggable(ids []string) []string {
	return slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return !mentionID.MatchString(id) })
}

// untyped are the tags starting with fragment whose recipient the line
// doesn't already name, in any of its aliases: a side and an ID, so the
// same name on the other side stays offered.
func (m *Model) untyped(tags []string, q pickQuery) []string {
	named := make(map[tagIdentity]bool)
	for _, word := range m.finishedTags() {
		named[identityOf(parseTag(word))] = true
	}
	kept := slices.DeleteFunc(slices.Clone(tags), func(tag string) bool { return named[identityOf(parseTag(tag))] })
	return withPrefix(kept, q.fragment)
}

// finishedTags are the draft's leading @words, without their @, that a
// space has finished: the one still being typed counts only once it is.
func (m *Model) finishedTags() []string {
	line, _, _ := strings.Cut(m.compose.editor.Value(), "\n")
	words := strings.Fields(line)
	if len(words) > 0 && !strings.HasSuffix(line, " ") {
		words = words[:len(words)-1]
	}
	var tags []string
	for _, w := range words {
		if !strings.HasPrefix(w, "@") {
			break
		}
		tags = append(tags, strings.TrimPrefix(w, "@"))
	}
	return tags
}

// tagIdentity is whom a ⇄ tag names: this side or theirs, and the ID.
type tagIdentity struct {
	here bool
	id   string
}

// identityOf resolves a tag as a ⇄ tab reads it: AGENT@here and a bare
// operator are ours; a bare agent and any other scope are theirs.
func identityOf(t tagWord) tagIdentity {
	if !t.scoped {
		return tagIdentity{here: t.operator(), id: t.word}
	}
	return tagIdentity{here: t.scope == hereScope, id: t.name}
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
