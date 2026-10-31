package chat

import (
	tea "charm.land/bubbletea/v2"
)

// wheelLines is how far one wheel notch moves.
const wheelLines = 3

// wheel scrolls what is under the pointer: an open panel, a composer taller
// than its visible rows, a ⇄ tab's messages, otherwise the transcript, with
// the same anchoring as PgUp/PgDn. The wheel at the top loads older history like PgUp; the
// wheel at the bottom resumes following.
func (m *Model) wheel(msg tea.MouseWheelMsg) tea.Cmd {
	direction := 0
	switch msg.Button {
	case tea.MouseWheelUp:
		direction = -1
	case tea.MouseWheelDown:
		direction = 1
	default:
		return nil
	}
	if m.tooSmall() {
		return nil
	}
	l := m.layout()
	switch {
	case m.panel != nil:
		m.panel.scrollLines(direction*wheelLines, l.transcript)
		return nil
	case m.overComposer(msg.Y, l):
		m.scrollComposer(direction)
		return nil
	case m.conv.selected.isContact():
		return m.contactMove(lineDelta(direction * wheelLines))
	}
	return m.scrollLines(direction * wheelLines)
}

// overComposer reports a pointer on the composer rows while the draft has
// more visual rows than the composer shows.
func (m *Model) overComposer(y int, l frameLayout) bool {
	if m.compose.cmd != nil {
		return false
	}
	bottom := m.height - 1 - l.picker // the footer and the picker sit below
	if l.boxed {
		bottom--
	}
	if y < bottom-l.composer || y >= bottom {
		return false
	}
	return m.composerOverflows(l)
}

// composerOverflows measures the draft in an unbounded copy of the editor:
// its own wrapping decides, soft wraps and the cursor row after an exactly
// full row included.
func (m *Model) composerOverflows(l frameLayout) bool {
	probe := newComposer().editor
	probe.MaxHeight, probe.MaxContentHeight = 0, 0
	probe.SetWidth(m.editorWidth(l.boxed))
	probe.SetValue(m.compose.editor.Value())
	return probe.Height() > l.composer
}

// scrollComposer moves the cursor through the draft with the editor's own
// line keys until its viewport has shifted by a notch or the cursor stalls;
// the editor keeps its viewport on the cursor.
func (m *Model) scrollComposer(direction int) {
	start := m.compose.editor.ScrollYOffset()
	for range composerMaxContentLines {
		if abs(m.compose.editor.ScrollYOffset()-start) >= wheelLines {
			return
		}
		if !m.stepComposer(direction) {
			return
		}
	}
}

// stepComposer moves the cursor one visual row and reports whether it moved.
// A stalled step down jumps to the end of the draft: the row after an
// exactly full row is only reached from there.
func (m *Model) stepComposer(direction int) bool {
	e := &m.compose.editor
	key := tea.KeyPressMsg{Code: tea.KeyUp}
	if direction > 0 {
		key = tea.KeyPressMsg{Code: tea.KeyDown}
	}
	line, row := e.Line(), e.LineInfo().RowOffset
	m.updateEditor(key)
	if e.Line() != line || e.LineInfo().RowOffset != row {
		return true
	}
	if direction > 0 {
		e.MoveToEnd()
	}
	return false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// scrollLines moves the transcript by delta lines (negative: up).
func (m *Model) scrollLines(delta int) tea.Cmd {
	height := max(1, m.layout().transcript)
	lines := m.transcript()
	start := m.top(lines, height)
	bottom := max(0, len(lines)-height)
	if delta < 0 && start == 0 {
		m.anchor(lines, 0)
		m.startOlderRun()
		return m.loadOlder(nil)
	}
	next := start + delta
	if next >= bottom {
		m.follow()
		return nil
	}
	m.anchor(lines, max(0, next))
	return nil
}

// scrollLines moves a panel by lines, bounded like a page.
func (p *panel) scrollLines(delta, height int) {
	page := max(1, height-1)
	p.offset = max(0, min(p.offset+delta, max(0, p.rows-page)))
}
