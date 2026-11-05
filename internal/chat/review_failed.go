package chat

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// The Failed section of /reviews (#298): reviews here whose publication
// failed or is uncertain, and this side's requests that failed. Red in the
// footer goes off only once every failure of the counted set is on screen:
// all its pages read, every one naming the same set, that set the one the
// counts name, while the panel is open. Showing never retries or resolves
// anything; the rows stay red.

// failedPage keeps one page of failures. A page of another set than the
// pages before it means the set changed while paging: the section is read
// again from its first page, and nothing is taken as shown.
func (m *Model) failedPage(page *protocol.ReviewFailedPage, first bool) tea.Cmd {
	f := &m.sections.failed
	if !first && page.Token != m.sections.failToken {
		f.items, f.more, f.next = nil, false, ""
		return m.readSection(protocol.ReviewOpFailed, true)
	}
	if first {
		m.sections.failToken = page.Token
	}
	if m.reviewSig.stale {
		m.sections.failToken = "" // read against counts that may be old: shown, but no evidence
	}
	for _, it := range page.Items {
		m.sections.ref(failedKey(it))
	}
	f.add(page.Items, first, failedKey)
	f.total, f.more, f.next = page.Total, page.More, page.Next
	m.markFailuresShown()
	return nil
}

// markFailuresShown turns red off when the open panel shows every failure
// of the set the counts name.
func (m *Model) markFailuresShown() {
	if m.failuresCounted() && m.failedPagesComplete() {
		m.failuresShown(m.reviewSig.counts.FailedToken)
	}
}

// failuresCounted: the panel is open, and counts read now name failures.
func (m *Model) failuresCounted() bool {
	c := m.reviewSig.counts
	if !m.reviewsPanelOpen() || m.reviewSig.stale {
		return false
	}
	return c != nil && c.Failed > 0
}

// failedPagesComplete: every page of one failure set is read, against
// current counts, and that set is the one counted now.
func (m *Model) failedPagesComplete() bool {
	f := &m.sections.failed
	if f.more || f.firstIn {
		return false // not all of it read yet
	}
	if len(f.items) != f.total {
		return false
	}
	token := m.sections.failToken
	return token != "" && token == m.reviewSig.counts.FailedToken
}

// syncFailedSection keeps an open panel's Failed section with the counts
// read now: read again when they name another failure set than the pages
// shown, emptied when nothing failed.
func (m *Model) syncFailedSection() tea.Cmd {
	c := m.reviewSig.counts
	switch {
	case !m.reviewsPanelOpen() || m.reviewSig.stale:
		return nil
	case !m.failuresWanted():
		return m.refreshFailed()
	case m.sections.failToken != c.FailedToken:
		return m.readSection(protocol.ReviewOpFailed, true)
	}
	return nil
}

// failedReviewHere is the review a failed ref names, when it is one here.
func (m *Model) failedReviewHere(ref sectionRef) (string, bool) {
	it, ok := findItem(m.sections.failed.items, failedKey, m.sections.byRef[ref])
	return it.ID, ok && it.Kind == protocol.FailedReview
}

// pagedSections are the sections /reviews more reads.
func (m *Model) pagedSections() []string {
	if m.failuresServed() {
		return []string{"needs", "asked", "failed"}
	}
	return []string{"needs", "asked"}
}

// failedShown: the panel has a Failed section, the broker serving
// failures and some counted or shown.
func (m *Model) failedShown() bool {
	return m.failuresServed() && (m.failuresWanted() || len(m.sections.failed.items) > 0)
}

// failedRows are the Failed section's rows, each red and still.
func (m *Model) failedRows() []string {
	s := &m.sections.failed
	if len(s.items) == 0 {
		return []string{mutedStyle.Render("  none read yet")}
	}
	var rows []string
	for _, it := range s.items {
		row := fmt.Sprintf("%s%s  %s#%s  %s  %s", m.rowMark(dotRed), m.sections.ref(failedKey(it)), safe(it.Repository),
			safe(it.Change), m.failedWho(it), safe(it.State))
		if it.Reason != "" {
			row += "  " + safe(it.Reason)
		}
		rows = append(rows, row+"  "+mutedStyle.Render(age(it.UpdatedAt)))
		if it.Kind == protocol.FailedAsk { // only their side's refusal has their operator's fix
			rows = append(rows, fixRows(it.Reason, it.Reviewer)...)
		}
	}
	return append(rows, moreRow(s.more, "failed")...)
}

// failedWho is who a failure's review is with: a reviewer here, or one in
// the paired workspace asked.
func (m *Model) failedWho(it protocol.ReviewFailedItem) string {
	if it.Kind == protocol.FailedReview {
		return safe(it.Reviewer) + " here"
	}
	return safe(it.Reviewer) + "@" + safe(m.contactTitle(contactKey(it.Contact)))
}

// heading is a section's title with its most urgent row's dot.
func (m *Model) heading(style lipgloss.Style, name string, d reviewDot) string {
	if d == dotNone {
		return style.Render(name)
	}
	return style.Render(name) + " " + strings.TrimSpace(m.rowMark(d))
}

// itemsDot is the most urgent dot of a section's items.
func itemsDot[T any](items []T, dot func(T) reviewDot) reviewDot {
	worstDot := dotNone
	for _, it := range items {
		worstDot = worst(worstDot, dot(it))
	}
	return worstDot
}

// reviewLine is a review here as a panel row, led by its dot.
func (m *Model) reviewLine(r protocol.ReviewView) string {
	return m.rowMark(reviewHereDot(r)) + strings.TrimPrefix(reviewRow(r), "  ")
}
