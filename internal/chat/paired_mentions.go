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
		a = m.placeWord(a, parseTag(word))
		if a.problem != "" {
			return a
		}
	}
	switch {
	case a.homeTagged() && !m.pairedMentions():
		a.problem = "tagging your agents or operator in a ⇄ tab is unsupported by this broker; upgrade it"
	case a.theirOperator && len(a.to) > 0:
		a.problem = "a message there goes to their operator or to their agents: drop @operator@" + safe(m.contactTitle(m.conv.selected)) + " or the agents"
	}
	return a
}

// pairedMentions: the broker takes a ⇄ draft's tags here (#278).
func (m *Model) pairedMentions() bool {
	return m.session.HasCapability(protocol.CapabilityPairedMentions)
}

// tagWord is one leading @word of a ⇄ draft: as written, and the name and
// scope it splits into when it names one. An agent ID with an @ of its own
// names no scope: it is theirs, as written.
type tagWord struct {
	word, name, scope string
	scoped            bool
}

func parseTag(word string) tagWord {
	name, scope, scoped := strings.Cut(word, "@")
	return tagWord{word: word, name: name, scope: scope, scoped: scoped && mentionID.MatchString(name)}
}

// agent is the agent the tag names, on whichever side.
func (t tagWord) agent() string {
	if t.scoped {
		return t.name
	}
	return t.word
}

// qualified: AGENT@SCOPE, as a ⇄ tab writes one.
func (t tagWord) qualified() bool { return t.scoped && t.scope != "" }

// operator: the tag names an operator, on whichever side.
func (t tagWord) operator() bool { return t.agent() == protocol.OperatorID }

// placeWord puts one leading @word where it belongs.
func (m *Model) placeWord(a address, t tagWord) address {
	switch {
	case !t.scoped && t.operator():
		a.operatorHere = true
	case !t.scoped:
		return m.addTheirs(a, t)
	case t.scope == hereScope:
		return m.placeHome(a, t)
	case m.isThisContact(t):
		return m.placeThere(a, t)
	default:
		a.problem = "@" + safe(t.word) + " names another workspace; this tab writes to " + safe(m.contactTitle(m.conv.selected))
	}
	return a
}

// placeThere tags their operator, the default audience, or one of their
// agents.
func (m *Model) placeThere(a address, t tagWord) address {
	if t.operator() {
		a.theirOperator = true
		return a
	}
	return m.addTheirs(a, t)
}

// addTheirs adds one of their agents: once each where the broker takes tags
// here, else as written, so an older broker's check still sees a repeat.
func (m *Model) addTheirs(a address, t tagWord) address {
	if m.pairedMentions() {
		a.to = appendOnce(a.to, t.agent())
	} else {
		a.to = append(a.to, t.agent())
	}
	return a
}

// placeHome tags one of this side's agents, or its operator.
func (m *Model) placeHome(a address, t tagWord) address {
	switch {
	case t.operator():
		a.operatorHere = true
	case m.hasAgent(t):
		a.here = appendOnce(a.here, t.name)
	default:
		a.problem = safe(t.name) + " isn't an agent here"
	}
	return a
}

// isThisContact: the tag's scope names the tab's workspace, by its key or
// its name.
func (m *Model) isThisContact(t tagWord) bool {
	key := m.conv.selected
	return t.scope == key.contact || t.scope == m.contactTitle(key)
}

// hasAgent: the tag names an agent this side knows now.
func (m *Model) hasAgent(t tagWord) bool {
	return slices.ContainsFunc(m.agents.list, func(a protocol.AgentPresence) bool { return a.AgentID == t.name })
}

// appendOnce adds v unless the list holds it already.
func appendOnce[T comparable](list []T, v T) []T {
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
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

// writableMention: an ID a mention carries, plain or scoped.
func writableMention(id string) bool { return mentionID.MatchString(id) || parseTag(id).qualified() }

// onlyHomeTags: the draft tags someone here and says nothing.
func (a address) onlyHomeTags() bool {
	if len(a.to) > 0 || !a.homeTagged() {
		return false
	}
	return strings.TrimSpace(a.text) == ""
}
