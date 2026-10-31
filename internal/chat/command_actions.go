package chat

import (
	"fmt"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// target resolves "#n" to a loaded current-epoch entry, or fallback's entry
// when the argument is empty.
func (m *Model) target(arg argument, fallback func() *entry) (*entry, string) {
	if arg.empty() {
		return defaultTarget(fallback)
	}
	n, err := arg.target()
	if err != nil {
		return nil, err.Error()
	}
	return m.entryForSeq(n)
}

func defaultTarget(fallback func() *entry) (*entry, string) {
	if e := fallback(); e != nil {
		return e, ""
	}
	return nil, "no message to use; give #n"
}

// entryForSeq finds the current-epoch entry numbered n. Entries from an older
// epoch can share the number; they are skipped, and reported only when no
// current entry has it.
func (m *Model) entryForSeq(n uint64) (*entry, string) {
	for _, e := range m.timeline.order {
		if e.msg.Seq == n && e.epoch == m.epoch {
			return e, ""
		}
	}
	if m.hasSeq(n) {
		return nil, fmt.Sprintf("#%d is from before the broker restarted; /history reloads it", n)
	}
	return nil, fmt.Sprintf("#%d isn't loaded; /history loads older messages", n)
}

func (m *Model) hasSeq(n uint64) bool {
	for _, e := range m.timeline.order {
		if e.msg.Seq == n {
			return true
		}
	}
	return false
}

// targetable reports a received entry read in the current epoch.
func (m *Model) targetable(e *entry) bool {
	return e.direction == "received" && e.epoch == m.epoch
}

func (m *Model) newestReceived(want func(*entry) bool) func() *entry {
	return func() *entry {
		for i := len(m.timeline.order) - 1; i >= 0; i-- {
			if e := m.timeline.order[i]; m.targetable(e) && want(e) {
				return e
			}
		}
		return nil
	}
}

func anyEntry(*entry) bool { return true }

func noEntry() *entry { return nil }

func (m *Model) hasReceived() bool { return m.newestReceived(anyEntry)() != nil }

func (m *Model) hasSent() bool {
	for _, e := range m.timeline.order {
		if e.direction == "sent" {
			return true
		}
	}
	return false
}

// unacknowledged is a received entry the operator hasn't acknowledged, queued
// or sent an acknowledgment for.
func (m *Model) unacknowledged(e *entry) bool {
	return m.ackState(e) == AckNeeded
}

func (m *Model) replyRun(arg argument) tea.Cmd {
	m.replyCommand(arg)
	return nil
}

// replyCommand freezes a reply to a received message as the draft's
// audience; it never widens an already addressed draft.
func (m *Model) replyCommand(arg argument) {
	if arg == "cancel" {
		m.cancelReply()
		m.status = "reply cancelled"
		return
	}
	e, reason := m.target(arg, m.newestReceived(m.inSelected))
	if reason == "" {
		reason = m.replyRefusal(e)
	}
	if reason != "" {
		m.status = reason
		return
	}
	if !m.moveTo(replyView(e)) {
		return
	}
	m.compose.reply = &replyTarget{messageID: e.msg.MessageID, sender: e.msg.From, seq: e.msg.Seq, public: isPublic(e), room: e.msg.Room}
	m.status = ""
}

// inSelected restricts reply targets to the open conversation: a room's own
// messages, a 1:1's or group's senders'; in All any received message.
func (m *Model) inSelected(e *entry) bool {
	key := m.conv.selected
	if key.isRoom() {
		return e.msg.Room == key.room
	}
	return key.common() || e.msg.From == key.agent() || slices.Contains(key.group(), e.msg.From)
}

func (m *Model) replyRefusal(e *entry) string {
	if e.direction != "received" {
		return "replies go to received messages; mention agents for a new message"
	}
	if m.compose.addressed() {
		return "draft is already addressed; remove the mentions or /to first"
	}
	if m.conv.selected == replyView(e) {
		return "" // the reply stays in this view
	}
	return m.switchRefusal(replyView(e))
}

// isPublic reports an agent's public "*" message (v7): everyone saw it, so a
// reply to it goes to everyone too.
func isPublic(e *entry) bool {
	return e.direction == "received" && e.msg.To == protocol.Broadcast
}

// replyView is where a reply is composed: All for a public message, otherwise
// the sender's 1:1.
func replyView(e *entry) viewKey {
	if isPublic(e) && e.msg.Room != "" {
		return roomKey(e.msg.Room) // a reply stays in its parent's room
	}
	if isPublic(e) {
		return allKey
	}
	return agentKey(e.msg.From)
}

// switchRefusal is why a reply can't move into its view: a draft addressed
// to the current 1:1, or an unsent draft waiting there.
func (m *Model) switchRefusal(key viewKey) string {
	if !m.conv.selected.common() && !m.compose.draftEmpty() {
		return "draft is already addressed; send or clear it first"
	}
	if m.savedDraft(key) {
		return viewName(key) + " has an unsent draft: switch to it to send or clear it first"
	}
	return ""
}

// viewName is a conversation's name in a status line.
func viewName(key viewKey) string {
	return key.label()
}

// moveTo switches a reply into its view; an unaddressed draft typed in All
// moves with it and All's draft becomes empty.
func (m *Model) moveTo(key viewKey) bool {
	if m.conv.selected == key {
		return true
	}
	m.refreshDirectory()
	text, pasted := m.compose.editor.Value(), m.compose.pasted
	if m.conv.selected.common() {
		m.compose.resetDraft()
	}
	if !m.selectView(key) {
		if m.conv.selected.common() {
			m.compose.setDraft(text, pasted)
		}
		return false
	}
	if text != "" {
		m.compose.setDraft(text, pasted)
	}
	return true
}

// addressed reports a draft with a /to audience or typed leading mentions.
func (c *composer) addressed() bool {
	if c.to != nil {
		return true
	}
	return !c.pasted && len(parsePrefix(c.editor.Value()).tokens) > 0
}

func (m *Model) historyCommand(argument) tea.Cmd {
	if m.cutoff != 0 {
		// /clear only hid rows; /history shows them again before loading more.
		m.cutoff = 0
		m.status = "cleared messages shown again"
		return nil
	}
	return m.loadOlder(m.newestGap())
}

// newestGap is the newest entry with possibly missing messages below it.
func (m *Model) newestGap() *entry {
	for i := len(m.timeline.order) - 1; i >= 0; i-- {
		if e := m.timeline.order[i]; e.gapBelow {
			return e
		}
	}
	return nil
}

func (m *Model) stagesCommand(arg argument) tea.Cmd {
	e, reason := m.target(arg, noEntry)
	if e != nil && e.direction != "sent" {
		e, reason = nil, "stages exist only for sent messages"
	}
	if e == nil {
		m.status = reason
		return nil
	}
	m.panel = &panel{kind: panelStages, seq: e.msg.Seq, id: e.msg.MessageID}
	return m.loadStages(e)
}

func (m *Model) toCommand(arg argument) tea.Cmd {
	c := &m.compose
	switch {
	case !m.conv.selected.common():
		m.status = "switch to All for an explicit audience"
	case c.reply != nil:
		m.status = "use /reply cancel first"
	case arg.empty():
		c.to = nil
		m.status = "audience cleared"
	case !c.pasted && len(parsePrefix(c.editor.Value()).tokens) > 0:
		m.status = "use either /to or @mentions, not both"
	default:
		m.setTo(arg)
	}
	return nil
}

func (m *Model) setTo(arg argument) {
	scope, err := ParseRecipients(string(arg)) // toArg already required a JSON array
	switch {
	case err != nil:
		m.status = err.Error()
	case isBroadcast(scope):
		m.status = "a message without mentions already goes to everyone"
	case scope.To != "":
		m.compose.to = []string{scope.To}
	default:
		m.compose.to = scope.Group
	}
}

func (m *Model) clearCommand(argument) tea.Cmd {
	if n := len(m.timeline.order); n > 0 {
		m.cutoff = m.timeline.order[n-1].msg.Seq
	}
	m.follow()
	return nil
}

func (m *Model) copyCommand(arg argument) tea.Cmd {
	if agent, ok := arg.refAgent(); ok {
		return m.copyRef(agent)
	}
	e, reason := m.target(arg, noEntry)
	if e != nil && e.omitted {
		e, reason = nil, "that message's text isn't available"
	}
	if e == nil {
		m.status = reason
		return nil
	}
	m.status = fmt.Sprintf("copy of #%d requested (the terminal may not support it)", e.msg.Seq)
	// OSC 52 carries the text base64-encoded; peer bytes never reach the
	// terminal raw.
	return tea.SetClipboard(payloadText(e.msg.Payload))
}

func (m *Model) agentsCommand(argument) tea.Cmd {
	m.panel = &panel{kind: panelAgents}
	return nil
}
