package chat

import (
	"slices"
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Paired mentions (#278): in a ⇄ tab, @AGENT@here tags one of this side's
// agents and @operator this side's operator, besides the paired workspace's
// audience; @AGENT and @AGENT@WS stay theirs, @operator@WS is their
// operator. A tag naming another workspace, or an agent not here, holds the
// draft, as does any tag here on a broker that can't carry it.

// hereScope is the scope naming this workspace in a ⇄ tab.
const hereScope = "here"

// homeTagged: the draft tags someone here too.
func (a address) homeTagged() bool { return len(a.here) > 0 || a.operatorHere }

// resolveAddress sorts a ⇄ draft's leading words into their agents, ours and
// our operator, or why the draft is held.
func (m *Model) resolveAddress(a address) address {
	raw := a.to
	a.to = nil
	for _, word := range raw {
		a = m.placeWord(a, word)
		if a.problem != "" {
			return a
		}
	}
	if a.homeTagged() && !m.pairedMentions() {
		a.problem = "tagging your agents or operator in a ⇄ tab is unsupported by this broker; upgrade it"
	}
	return a
}

// pairedMentions: the broker takes a ⇄ draft's tags here (#278).
func (m *Model) pairedMentions() bool {
	return m.session.HasCapability(protocol.CapabilityPairedMentions)
}

// addTheirs adds one of their agents: once each where the broker takes tags
// here, else as written, so an older broker's check still sees a repeat.
func (m *Model) addTheirs(to []string, name string) []string {
	if m.pairedMentions() {
		return appendOnce(to, name)
	}
	return append(to, name)
}

// placeWord puts one leading @word where it belongs.
func (m *Model) placeWord(a address, word string) address {
	name, scope, scoped := strings.Cut(word, "@")
	scoped = scoped && mentionID.MatchString(name) // an agent ID with an @ of its own is theirs, as written
	switch {
	case !scoped && word == protocol.OperatorID:
		a.operatorHere = true
	case !scoped:
		a.to = m.addTheirs(a.to, word)
	case scope == hereScope:
		return m.placeHome(a, name)
	case m.isThisContact(scope):
		if name != protocol.OperatorID { // @operator@WS is their operator: the default audience
			a.to = m.addTheirs(a.to, name)
		}
	default:
		a.problem = "@" + safe(word) + " names another workspace; this tab writes to " + safe(m.contactTitle(m.conv.selected))
	}
	return a
}

// placeHome tags one of this side's agents, or its operator.
func (m *Model) placeHome(a address, name string) address {
	switch {
	case name == protocol.OperatorID:
		a.operatorHere = true
	case m.isLocalAgent(name):
		a.here = appendOnce(a.here, name)
	default:
		a.problem = safe(name) + " isn't an agent here"
	}
	return a
}

// isThisContact: scope names the tab's workspace, by its key or its name.
func (m *Model) isThisContact(scope string) bool {
	key := m.conv.selected
	return scope == key.contact || scope == m.contactTitle(key)
}

// isLocalAgent: an agent this side knows now.
func (m *Model) isLocalAgent(id string) bool {
	return slices.ContainsFunc(m.agents.list, func(a protocol.AgentPresence) bool { return a.AgentID == id })
}

func appendOnce(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

// homeScope names who here a draft tags: agent@here, operator@here.
func homeScope(a address) string {
	parts := make([]string, 0, len(a.here)+1)
	for _, id := range a.here {
		parts = append(parts, safe(id)+"@"+hereScope)
	}
	if a.operatorHere {
		parts = append(parts, protocol.OperatorID+"@"+hereScope)
	}
	return strings.Join(parts, ", ")
}

// scopedMentions are the @ picker's choices beyond their bare agents: ours
// as AGENT@here once something is typed, and theirs qualified once the
// typing names a scope; a workspace this side calls "here" is named by its
// key.
func (m *Model) scopedMentions(contact, fragment string) []string {
	if fragment == "" || !m.session.HasCapability(protocol.CapabilityPairedMentions) {
		return nil
	}
	var scoped []string
	for _, id := range m.localAgents("") {
		scoped = append(scoped, id+"@"+hereScope)
	}
	if strings.Contains(fragment, "@") {
		qualifier := m.contactTitle(contactKey(contact))
		if qualifier == hereScope || !mentionID.MatchString(qualifier) {
			qualifier = contact
		}
		for _, id := range m.sharedByContact(contact) {
			scoped = append(scoped, id+"@"+qualifier)
		}
	}
	slices.Sort(scoped)
	return withPrefix(scoped, fragment)
}

// writableMention: an ID a mention carries, plain or scoped.
func writableMention(id string) bool { return mentionID.MatchString(id) || scopedMention(id) }

// onlyHomeTags: the draft tags someone here and says nothing.
func (a address) onlyHomeTags() bool {
	return len(a.to) == 0 && a.homeTagged() && strings.TrimSpace(a.text) == ""
}

// scopedMention: AGENT@SCOPE, as a ⇄ tab writes one.
func scopedMention(id string) bool {
	name, scope, ok := strings.Cut(id, "@")
	return ok && mentionID.MatchString(name) && scope != ""
}
