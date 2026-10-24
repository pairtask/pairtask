package chat

import (
	"slices"

	"github.com/pairtask/pairtask/internal/protocol"
)

// ConversationAll names the common chat in the state view and in /chat. In
// the model the common chat is the empty key.
const ConversationAll = "all"

// savedViewsMax bounds the saved conversation state, All included; the live
// directory of agents is not bounded here.
const savedViewsMax = 64

// SourceConversation is the audience a 1:1 view gives a draft by itself.
const SourceConversation = "conversation"

// viewKey names a conversation: an agent ID, or allKey for the common chat,
// which no valid agent ID can equal.
type viewKey string

const allKey viewKey = ""

func (k viewKey) common() bool { return k == allKey }

func (k viewKey) agent() string { return string(k) }

// view is one conversation's saved state: where it was scrolled, what it
// hides and searches for, and its own draft with the draft's reply target
// and explicit audience. The selected conversation's state lives in the
// model's fields; it is saved here when another one is selected.
type view struct {
	port    viewport
	cutoff  uint64
	find    string
	found   uint64
	compose composer
	used    int // selection order, for eviction
}

// conversations is the directory of agents by first appearance, the
// selected conversation and the saved views.
type conversations struct {
	order    []string // agent IDs by first appearance in this session
	selected viewKey
	saved    map[viewKey]*view
	uses     int
}

// ConversationView is one entry of the conversation bar. ID is the raw agent
// ID and empty for the common chat, which All marks, so an agent literally
// named "all" is never confused with it.
type ConversationView struct {
	ID       string
	All      bool
	Position int
	Unread   int
	Selected bool
	Draft    bool
}

// refreshDirectory keeps the directory in step with presence and the loaded
// timeline: agents that vanished from both leave it, new ones append, and
// the selected agent stays while selected. Positions are displayed order,
// so they move only when an agent leaves.
func (m *Model) refreshDirectory() {
	live := m.liveAgents()
	m.conv.order = pruneOrder(m.conv.order, live)
	m.conv.order = appendUnseen(m.conv.order, live)
}

// pruneOrder keeps the directory entries that are still live.
func pruneOrder(order []string, live agentSet) []string {
	kept := order[:0]
	for _, id := range order {
		if live.has[id] {
			kept = append(kept, id)
		}
	}
	return kept
}

// appendUnseen adds live agents the directory doesn't list yet, in order of
// first appearance.
func appendUnseen(order []string, live agentSet) []string {
	for _, id := range live.seen {
		if !slices.Contains(order, id) {
			order = append(order, id)
		}
	}
	return order
}

// agentSet is an ordered set of agent IDs.
type agentSet struct {
	has  map[string]bool
	seen []string
}

func (s *agentSet) add(id string) {
	if reservedID(id) || s.has[id] {
		return
	}
	s.has[id] = true
	s.seen = append(s.seen, id)
}

func (s *agentSet) addAll(ids []string) {
	for _, id := range ids {
		s.add(id)
	}
}

// reservedID is an ID that never names a conversation.
func reservedID(id string) bool {
	return id == "" || id == protocol.OperatorID || id == protocol.Broadcast
}

// liveAgents are the agents the directory lists: in the loaded timeline, in
// presence, selected, or holding a protected view, in that order of first
// appearance. A view with an unsent draft or a reply binding stays reachable
// even when its agent left presence and the timeline.
func (m *Model) liveAgents() agentSet {
	live := agentSet{has: map[string]bool{}}
	m.addTimelineAgents(&live)
	m.addPresentAgents(&live)
	live.add(m.conv.selected.agent())
	m.addProtectedViews(&live)
	return live
}

func (m *Model) addTimelineAgents(live *agentSet) {
	for _, e := range m.timeline.order {
		live.addAll(entryAgents(e))
	}
}

func (m *Model) addPresentAgents(live *agentSet) {
	for _, a := range m.agents.list {
		live.add(a.AgentID)
	}
}

func (m *Model) addProtectedViews(live *agentSet) {
	for key, v := range m.conv.saved {
		if v.protected() {
			live.add(key.agent())
		}
	}
}

// entryAgents lists the agents an entry belongs to: its sender, or its
// accepted recipients.
func entryAgents(e *entry) []string {
	if e.direction == "received" {
		return []string{e.msg.From}
	}
	ids := append([]string(nil), e.msg.Group...)
	if e.msg.To != "" && e.msg.To != protocol.Broadcast {
		ids = append(ids, e.msg.To)
	}
	for id := range e.stages {
		ids = append(ids, id)
	}
	return ids
}

// inView reports whether an entry belongs to a conversation: All holds
// everything; a 1:1 with X holds what X sent and what X is an accepted
// recipient of. Presence never implies membership; a truncated stage list
// means "not yet known".
func (m *Model) inView(e *entry, key viewKey) bool {
	if key.common() {
		return true
	}
	id := key.agent()
	if e.direction == "received" {
		return e.msg.From == id
	}
	if e.msg.To == id || slices.Contains(e.msg.Group, id) {
		return true
	}
	_, ok := e.stages[id]
	return ok
}

// selectView switches to a conversation, saving the current one. A new view
// is allocated only when opened; beyond the bound, an unselected view with
// no draft and no reply binding is evicted, and when none is, the switch is
// refused and nothing changes.
func (m *Model) selectView(key viewKey) bool {
	if key == m.conv.selected {
		return true
	}
	target := m.conv.saved[key]
	if target == nil && !m.openable(key) {
		return false
	}
	m.conv.uses++
	m.conv.saved[m.conv.selected] = &view{port: m.view, cutoff: m.cutoff, find: m.find, found: m.found, compose: m.compose, used: m.conv.uses}
	m.conv.selected = key
	if target == nil {
		target = &view{port: viewport{following: true}, compose: m.freshComposer()}
	}
	delete(m.conv.saved, key)
	m.view, m.cutoff, m.find, m.found, m.compose = target.port, target.cutoff, target.find, target.found, target.compose
	m.compose.picker = nil
	m.recalling = nil   // recall is a mode of the draft being edited
	m.history.run = nil // a filtered older-history run belongs to the view that started it
	m.markShown()
	return true
}

// openable reports whether a conversation without saved state may be
// opened: its agent is in the directory and a view slot is free.
func (m *Model) openable(key viewKey) bool {
	if !key.common() && !slices.Contains(m.conv.order, key.agent()) {
		m.status = "no conversation with " + safe(key.agent())
		return false
	}
	if !m.allocateView() {
		m.status = "conversation state limit reached; send or clear a saved draft first"
		return false
	}
	return true
}

// allocateView makes room for one more saved view.
func (m *Model) allocateView() bool {
	if len(m.conv.saved)+1 < savedViewsMax { // +1: the selected view counts
		return true
	}
	victim, best, found := allKey, 0, false
	for key, v := range m.conv.saved {
		if key.common() || v.protected() {
			continue // All is permanent; a draft or reply binding is never discarded
		}
		if !found || v.used < best {
			victim, best, found = key, v.used, true
		}
	}
	if !found {
		return false
	}
	delete(m.conv.saved, victim)
	return true
}

// protected views are never evicted: a nonempty draft or a reply binding.
func (v *view) protected() bool {
	return !v.compose.draftEmpty() || v.compose.reply != nil || v.compose.to != nil
}

// freshComposer is a new draft sized to the window, with the view's placeholder.
func (m *Model) freshComposer() composer {
	c := newComposer()
	c.setWidth(m.editorWidth(m.width >= boxMinWidth))
	c.editor.Placeholder = m.placeholder()
	return c
}

func (m *Model) placeholder() string {
	if m.conv.selected.common() {
		return "Message everyone · @agent for one · / for commands"
	}
	return "Message " + safe(m.conv.selected.agent()) + " · / for commands"
}

// savedDraft reports whether a conversation holds an unsent draft or a reply
// binding, selected or saved.
func (m *Model) savedDraft(key viewKey) bool {
	if key == m.conv.selected {
		return m.compose.protectedDraft()
	}
	if v := m.conv.saved[key]; v != nil {
		return v.protected()
	}
	return false
}

func (c *composer) protectedDraft() bool {
	return !c.draftEmpty() || c.reply != nil || c.to != nil
}

// cycleView moves the selection along the bar.
func (m *Model) cycleView(step int) {
	positions := m.positions()
	i := max(0, slices.Index(positions, m.conv.selected))
	m.selectView(positions[(i+step+len(positions))%len(positions)])
}

// positions is the bar in order: All, then the directory.
func (m *Model) positions() []viewKey {
	keys := []viewKey{allKey}
	for _, id := range m.conv.order {
		keys = append(keys, viewKey(id))
	}
	return keys
}

// jumpView selects a displayed position, 1 being All.
func (m *Model) jumpView(position int) {
	if positions := m.positions(); position >= 1 && position <= len(positions) {
		m.selectView(positions[position-1])
	}
}

// unreadIn counts the unread entries a conversation holds; for All that is
// every unread entry, counted once.
func (m *Model) unreadIn(key viewKey) int {
	n := 0
	for _, e := range m.timeline.order {
		if e.unread && m.inView(e, key) {
			n++
		}
	}
	return n
}

// markShown clears the unread flag of every entry with a line rendered while
// the selected conversation follows the bottom. Entries above the window or
// hidden by /clear stay unread.
func (m *Model) markShown() {
	if !m.view.following || !m.transcriptVisible() {
		return
	}
	lines := m.transcript()
	start := max(0, len(lines)-m.layout().transcript)
	for _, l := range lines[start:] {
		if e := m.timeline.entries[l.id]; e != nil {
			e.unread = false
		}
	}
}

// transcriptVisible reports whether transcript rows are on screen at all: not
// on a too-small terminal, not behind a panel, and only with rows to show.
func (m *Model) transcriptVisible() bool {
	if m.tooSmall() || m.panel != nil {
		return false
	}
	return m.layout().transcript > 0
}
