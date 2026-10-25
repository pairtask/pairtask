package chat

import (
	"slices"

	"github.com/pairtask/pairtask/internal/protocol"
)

// recall walks the operator's sent messages back into the composer so one
// can be corrected and sent again. It starts with Up in an empty draft, Up
// walks older, Down newer, Down past the newest restores the empty draft;
// an edit, Esc, a send or a view switch ends it. A recalled message is
// literal (never re-parsed for mentions) and carries its original scope:
// direct and group IDs exactly as sent, a broadcast as a broadcast.
type recall struct {
	entries []*entry // newest first
	index   int
}

// RecallView is the recall state, for tests and the CLI.
type RecallView struct {
	Active bool
	Index  int // 0 is the newest sent message
	Total  int
}

// recallable lists the sent messages the selected view can recall, newest
// first: in All every sent message; in a 1:1 with X only those whose scope
// was exactly direct X.
func (m *Model) recallable() []*entry {
	var out []*entry
	for i := len(m.timeline.order) - 1; i >= 0; i-- {
		e := m.timeline.order[i]
		if e.sentIntact() && m.recallableHere(e) {
			out = append(out, e)
		}
	}
	return out
}

// sentIntact reports a message of yours whose text history still has.
func (e *entry) sentIntact() bool {
	return e.direction == "sent" && !e.omitted
}

func (m *Model) recallableHere(e *entry) bool {
	key := m.conv.selected
	switch {
	case key.common():
		return true
	case key.isGroup():
		return len(e.msg.Group) > 0 && groupKey(e.msg.Group) == key
	}
	return e.msg.To == key.agent() && len(e.msg.Group) == 0
}

// recallUp starts recall from an empty draft, or walks one message older.
func (m *Model) recallUp() bool {
	if m.recalling == nil {
		if !m.compose.draftEmpty() {
			return false
		}
		entries := m.recallable()
		if len(entries) == 0 {
			m.status = "nothing to recall"
			return true
		}
		m.recalling = &recall{entries: entries}
		m.loadRecalled()
		return true
	}
	if m.recalling.index+1 < len(m.recalling.entries) {
		m.recalling.index++
		m.loadRecalled()
	}
	return true
}

// recallDown walks one message newer; past the newest the draft is empty
// again and recall ends.
func (m *Model) recallDown() bool {
	if m.recalling == nil {
		return false
	}
	if m.recalling.index == 0 {
		m.compose.resetDraft()
		m.recalling = nil
		m.afterEdit()
		return true
	}
	m.recalling.index--
	m.loadRecalled()
	return true
}

// loadRecalled puts the current entry into the composer with its scope.
func (m *Model) loadRecalled() {
	e := m.recalling.entries[m.recalling.index]
	m.compose.resetDraft()
	m.compose.setDraft(payloadText(e.msg.Payload), true)
	m.compose.to = m.recalledScope(e)
}

// recalledScope is the explicit audience a recalled message carries in All:
// its direct or group recipients as sent; a broadcast stays a broadcast, and
// a 1:1 view addresses the draft by itself.
func (m *Model) recalledScope(e *entry) []string {
	if !m.conv.selected.common() || e.msg.To == protocol.Broadcast {
		return nil
	}
	if len(e.msg.Group) > 0 {
		return slices.Clone(e.msg.Group)
	}
	return []string{e.msg.To}
}

// endRecall leaves recall mode keeping whatever is in the composer.
func (m *Model) endRecall() {
	m.recalling = nil
}

func (m *Model) recallView() *RecallView {
	if m.recalling == nil {
		return nil
	}
	return &RecallView{Active: true, Index: m.recalling.index, Total: len(m.recalling.entries)}
}
