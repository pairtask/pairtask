package chat

import (
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// PickerReview lists reviews for /review status and /review cancel typed
// without an ID (#208): status shows the picked review at once; cancel only
// puts "/review cancel ID" on the command line, and Enter there cancels.
const PickerReview = "review"

// pickReview opens the picker, reading the reviews first when none are
// known yet.
func (m *Model) pickReview(action string) tea.Cmd {
	if len(m.reviews.list) > 0 {
		m.openReviewPicker(action)
		return nil
	}
	m.reviews.loaded, m.reviews.pick = true, action
	m.status = "reading reviews…"
	return m.request(kindReview, protocol.Message{Type: protocol.TypeOperatorReview, ReviewOp: protocol.ReviewOpList}, protocol.ReviewOpList)
}

// pickAfterList opens the picker a list was read for.
func (m *Model) pickAfterList() {
	action := m.reviews.pick
	m.reviews.pick = ""
	if action != "" {
		m.openReviewPicker(action)
	}
}

// openReviewPicker lists the known reviews, the one shown or requested last
// highlighted.
func (m *Model) openReviewPicker(action string) {
	ids := m.reviewIDs()
	if len(ids) == 0 {
		m.status = "no reviews to " + action
		return
	}
	selected := max(0, slices.Index(ids, m.reviews.selected))
	if m.reviews.selected == "" {
		selected = max(0, slices.Index(ids, m.reviews.followed))
	}
	m.compose.picker = &picker{kind: PickerReview, items: ids, selected: selected, action: action}
	m.status = "pick a review: ↑/↓, Enter " + reviewPickVerb[action] + ", Esc closes"
}

var reviewPickVerb = map[string]string{"status": "shows it", "cancel": "puts /review cancel ID on the line"}

// reviewPickerKey handles keys while the review picker is open, whatever
// panel is showing; any other key closes it and goes on as usual.
func (m *Model) reviewPickerKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	p := m.compose.openPicker(PickerReview)
	if p == nil || m.compose.cmd != nil {
		return nil, false
	}
	switch msg.String() {
	case "enter", "tab":
		return m.pickedReview(p), true
	case "up", "down", "esc":
		m.pickerKey(msg)
		return nil, true
	}
	m.compose.picker = nil
	return nil, false
}

func (m *Model) pickedReview(p *picker) tea.Cmd {
	id := p.items[p.selected]
	m.compose.picker = nil
	if p.action == "status" {
		return m.reviewStatus(id)
	}
	m.openCommandLine()
	m.compose.cmd.set("review cancel " + id + " ")
	m.compose.picker = nil
	m.status = "Enter cancels review " + safe(reviewShort(id)) + "; a reason may follow the ID"
	return nil
}

// reviewLabel is a picker row: the review's list row.
func (m *Model) reviewLabel(id string) string {
	for _, r := range m.reviews.list {
		if r.ID == id {
			return reviewRow(r)
		}
	}
	return "  " + safe(id)
}
