package chat

import (
	"fmt"

	"github.com/pairtask/pairtask/internal/protocol"
)

// reviewRows are the reviews panel: one review in detail when one is
// selected, else the list. Everything an agent or a forge wrote is shown
// through safe(), and nothing here says whether a change may merge.
func (m *Model) reviewRows() []string {
	rows := []string{titleStyle.Render("Reviews") + mutedStyle.Render("  (Esc closes · /review status ID · /review cancel ID)")}
	if r, ok := m.selectedReview(); ok {
		return append(rows, reviewDetail(r)...)
	}
	if len(m.reviews.list) == 0 {
		return append(rows, "  no review requests")
	}
	for _, r := range m.reviews.list {
		rows = append(rows, reviewRow(r))
	}
	if m.reviews.partial {
		rows = append(rows, fmt.Sprintf("  (list is partial: %d more not shown)", m.reviews.total-len(m.reviews.list)))
	}
	return rows
}

func (m *Model) selectedReview() (protocol.ReviewView, bool) {
	d := m.reviews.detail
	if d == nil || d.ID != m.reviews.selected {
		return protocol.ReviewView{}, false
	}
	return *d, true
}

func reviewRow(r protocol.ReviewView) string {
	row := fmt.Sprintf("  %s  %s#%s  %s  %s", safe(reviewShort(r.ID)), safe(r.Spec.Repository), safe(r.Spec.Change), safe(r.Spec.Reviewer), safe(r.State))
	if r.Verdict != nil {
		row += "  verdict " + safe(r.Verdict.Outcome)
	}
	row += "  publication " + safe(r.Publication.State)
	if r.Stale != nil {
		row += "  stale"
	}
	return row
}

func reviewShort(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// reviewDetail is one review: its pins, the agent's verdict, and, apart
// from it, what the forge recorded.
func reviewDetail(r protocol.ReviewView) []string {
	rows := []string{reviewRow(r), "  review " + safe(r.ID)}
	if r.Revisions != nil {
		rows = append(rows, "  pinned base "+safe(r.Revisions.Base)+" head "+safe(r.Revisions.Head))
	}
	if r.Stale != nil {
		rows = append(rows, "  stale: the change is now base "+safe(r.Stale.Base)+" head "+safe(r.Stale.Head))
	}
	if r.Reason != "" {
		rows = append(rows, "  reason: "+safe(r.Reason))
	}
	rows = append(rows, verdictRows(r)...)
	return append(rows, publicationRows(r.Publication)...)
}

func verdictRows(r protocol.ReviewView) []string {
	v := r.Verdict
	if v == nil {
		return nil
	}
	rows := []string{fmt.Sprintf("  verdict (attempt %d): %s", r.Attempt, safe(v.Outcome)), "    " + safe(v.Summary)}
	for _, f := range v.Findings {
		rows = append(rows, fmt.Sprintf("    %s:%d  %s", safe(f.Path), f.Line, safe(f.Summary)))
	}
	return rows
}

func publicationRows(p protocol.Publication) []string {
	row := "  publication " + safe(p.State)
	if p.Reason != "" {
		row += " (" + safe(p.Reason) + ")"
	}
	rows := []string{row}
	if rc := p.Receipt; rc != nil {
		rows = append(rows, fmt.Sprintf("  published as %s by %s on %s", safe(recordedAs(*rc)), safe(rc.Identity), safe(reviewShort(rc.Revision))))
		if rc.URL != "" {
			rows = append(rows, "  "+safe(rc.URL))
		}
	}
	return rows
}

// recordedAs is what the forge recorded the review as.
func recordedAs(rc protocol.PublicationReceipt) string {
	if rc.Event != "" {
		return rc.Event
	}
	if rc.Formal {
		return "a formal review"
	}
	return "a comment"
}
