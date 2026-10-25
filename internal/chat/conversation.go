package chat

import (
	"encoding/json"
	"slices"
	"strings"

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

// viewKey names a conversation: the common chat (allKey), a 1:1 with an
// agent, or a group by its canonical members. The kinds are separate fields,
// so an agent literally named like a group, "group:x" or "all", stays a 1:1.
type viewKey struct {
	agentID string // a 1:1; empty otherwise
	members string // a group: its sorted, unique member IDs as JSON; empty otherwise
}

var allKey = viewKey{}

func agentKey(id string) viewKey { return viewKey{agentID: id} }

// groupKey is the conversation of a member set, in any order and with repeats.
func groupKey(ids []string) viewKey {
	members, _ := json.Marshal(canonicalGroup(ids))
	return viewKey{members: string(members)}
}

func canonicalGroup(ids []string) []string {
	members := slices.Clone(ids)
	slices.Sort(members)
	return slices.Compact(members)
}

func (k viewKey) common() bool { return k == allKey }

func (k viewKey) isGroup() bool { return k.members != "" }

// agent is a 1:1's agent ID, empty for All and for groups.
func (k viewKey) agent() string { return k.agentID }

// group is a group's members, nil otherwise.
func (k viewKey) group() []string {
	if !k.isGroup() {
		return nil
	}
	var members []string
	_ = json.Unmarshal([]byte(k.members), &members)
	return members
}

// label is how a conversation is named on screen.
func (k viewKey) label() string {
	switch {
	case k.common():
		return "All"
	case k.isGroup():
		names := k.group()
		for i, id := range names {
			names[i] = safe(id)
		}
		return strings.Join(names, ", ")
	}
	return safe(k.agentID)
}

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

// conversations is the directory of agents and groups by first appearance,
// the selected conversation, the saved views and the hidden ones.
type conversations struct {
	order    []viewKey // agents and groups by first appearance in this session
	selected viewKey
	saved    map[viewKey]*view
	hidden   map[viewKey]bool // closed with /close; UI only, reopened by traffic or /chat
	uses     int
}

// ConversationView is one entry of the conversation bar. ID is the raw agent
// ID and empty for the common chat, which All marks, so an agent literally
// named "all" is never confused with it.
type ConversationView struct {
	ID       string   // the agent ID of a 1:1; the member names of a group; empty for All
	Group    []string // a group's sorted members; nil for All and 1:1s
	All      bool
	Hidden   bool // closed with /close; not shown in the bar
	Position int  // bar position; 0 while hidden
	Unread   int
	Selected bool
	Draft    bool
}

// refreshDirectory keeps the directory in step with presence and the loaded
// timeline: conversations that vanished from both leave it, new ones append,
// and the selected one stays while selected. Positions are displayed order,
// so they move only when a conversation leaves.
func (m *Model) refreshDirectory() {
	live := m.liveViews()
	m.conv.order = pruneOrder(m.conv.order, live)
	m.conv.order = appendUnseen(m.conv.order, live)
}

// pruneOrder keeps the directory entries that are still live.
func pruneOrder(order []viewKey, live keySet) []viewKey {
	kept := order[:0]
	for _, key := range order {
		if live.has[key] {
			kept = append(kept, key)
		}
	}
	return kept
}

// appendUnseen adds live conversations the directory doesn't list yet, in
// order of first appearance.
func appendUnseen(order []viewKey, live keySet) []viewKey {
	for _, key := range live.seen {
		if !slices.Contains(order, key) {
			order = append(order, key)
		}
	}
	return order
}

// keySet is an ordered set of conversations.
type keySet struct {
	has  map[viewKey]bool
	seen []viewKey
}

func (s *keySet) add(key viewKey) {
	if nameless(key) || s.has[key] {
		return
	}
	s.has[key] = true
	s.seen = append(s.seen, key)
}

func (s *keySet) addAll(keys []viewKey) {
	for _, key := range keys {
		s.add(key)
	}
}

// nameless keys never get a conversation of their own: All, and 1:1s with a
// reserved ID.
func nameless(key viewKey) bool {
	if key.isGroup() {
		return false
	}
	return reservedID(key.agentID)
}

// reservedID is an ID that never names a conversation.
func reservedID(id string) bool {
	return id == "" || id == protocol.OperatorID || id == protocol.Broadcast
}

// liveViews are the conversations the directory lists: in the loaded
// timeline (a sender of public messages too, with an empty 1:1), in presence, selected, or holding a protected view, in that order
// of first appearance. A view with an unsent draft or a reply binding stays
// reachable even when nothing else lists it.
func (m *Model) liveViews() keySet {
	live := keySet{has: map[viewKey]bool{}}
	for _, e := range m.timeline.order {
		live.addAll(entryViews(e))
		if e.direction == "received" {
			live.add(agentKey(e.msg.From)) // a public sender stays reachable for a 1:1
		}
	}
	for _, a := range m.agents.list {
		live.add(agentKey(a.AgentID))
	}
	live.add(m.conv.selected)
	for key, v := range m.conv.saved {
		if v.protected() {
			live.add(key)
		}
	}
	return live
}

// entryViews lists the conversation an entry belongs to besides All. Only
// private traffic has one: a direct message is in the agent's 1:1, a group
// send in its group's conversation. Public "*" traffic, either way, is in All
// alone. A group is known only from the broker's member list, never from the
// to field.
func entryViews(e *entry) []viewKey {
	switch {
	case e.msg.To == protocol.Broadcast:
		return nil
	case e.direction == "received":
		return []viewKey{agentKey(e.msg.From)}
	case len(canonicalGroup(e.msg.Group)) == 1:
		return []viewKey{agentKey(e.msg.Group[0])} // a group of one is a direct message
	case len(e.msg.Group) > 0:
		return []viewKey{groupKey(e.msg.Group)}
	case e.msg.To != "":
		return []viewKey{agentKey(e.msg.To)}
	}
	return nil
}

// inView reports whether an entry belongs to a conversation: All holds
// everything; any other conversation holds only its own private traffic.
func (m *Model) inView(e *entry, key viewKey) bool {
	return key.common() || slices.Contains(entryViews(e), key)
}

// selectView switches to a conversation, saving the current one. A new view
// is allocated only when opened; beyond the bound, an unselected view with
// no draft and no reply binding is evicted, and when none is, the switch is
// refused and nothing changes.
func (m *Model) selectView(key viewKey) bool {
	delete(m.conv.hidden, key) // selecting a conversation always shows it
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
	if m.unlisted(key) {
		m.status = "no conversation with " + key.label()
		return false
	}
	if !m.allocateView() {
		m.status = "conversation state limit reached; send or clear a saved draft first"
		return false
	}
	return true
}

// unlisted is a 1:1 the directory doesn't know; All and groups are always openable.
func (m *Model) unlisted(key viewKey) bool {
	if key.common() || key.isGroup() {
		return false
	}
	return !slices.Contains(m.conv.order, key)
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
	return "Message " + m.conv.selected.label() + " · / for commands"
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

// cycleView moves the selection along the visible bar.
func (m *Model) cycleView(step int) {
	positions := m.positions()
	i := max(0, slices.Index(positions, m.conv.selected))
	m.selectView(positions[(i+step+len(positions))%len(positions)])
}

// positions is the visible bar in order: All, then the directory without the
// hidden conversations.
func (m *Model) positions() []viewKey {
	keys := []viewKey{allKey}
	for _, key := range m.conv.order {
		if !m.conv.hidden[key] {
			keys = append(keys, key)
		}
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
