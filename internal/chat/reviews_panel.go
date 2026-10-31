package chat

import (
	"fmt"
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

// reviewRows are the reviews panel: one review in detail when one is
// selected, else the list. Everything an agent or a forge wrote is shown
// through safe(), and nothing here says whether a change may merge.
func (m *Model) reviewRows() []string {
	rows := []string{titleStyle.Render("Reviews") + mutedStyle.Render("  (Esc closes · /review status|cancel picks one)")}
	if r, ok := m.selectedReview(); ok {
		return append(rows, m.reviewDetail(r)...)
	}
	if p := m.reviews.pending; p != nil && p.id == m.reviews.selected {
		return append(rows, m.pendingRows(*p)...)
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

// reviewDetail is one review in sections (#208): what was asked and pinned,
// the agent's verdict and findings as Markdown at the panel's width, and,
// apart from them, what the forge recorded.
func (m *Model) reviewDetail(r protocol.ReviewView) []string {
	rows := []string{reviewRow(r), "", sectionTitle("Target")}
	rows = append(rows, targetRows(r.Spec)...)
	rows = append(rows, "  review "+safe(r.ID))
	if r.Revisions != nil {
		rows = append(rows, "  pinned base "+safe(r.Revisions.Base), "  pinned head "+safe(r.Revisions.Head))
	}
	if r.Stale != nil {
		rows = append(rows, "  stale: the change is now base "+safe(r.Stale.Base)+" head "+safe(r.Stale.Head))
	}
	if r.Reason != "" {
		rows = append(rows, "  reason: "+safe(r.Reason))
	}
	rows = append(rows, m.verdictRows(r)...)
	return append(rows, publicationRows(r.Publication)...)
}

func sectionTitle(name string) string { return titleStyle.Render(name) }

// targetRows are the change, the reviewer, the scope and the policy asked for.
func targetRows(spec protocol.ReviewSpec) []string {
	rows := []string{"  " + safe(spec.Repository) + "#" + safe(spec.Change) + " · reviewer " + safe(spec.Reviewer)}
	if spec.Scope != "" {
		rows = append(rows, "  scope: "+safe(spec.Scope))
	}
	return append(rows, "  "+policyText(spec.Policy))
}

func policyText(p *protocol.ReviewPolicy) string {
	switch {
	case p == nil || p.Publish && p.Formal:
		return "publish as a formal review"
	case p.Publish:
		return "publish as a comment (--no-formal)"
	}
	return "not published (--no-publish)"
}

func (m *Model) verdictRows(r protocol.ReviewView) []string {
	v := r.Verdict
	if v == nil {
		return nil
	}
	rows := []string{"", sectionTitle("Verdict") + "  " + safe(v.Outcome) + mutedStyle.Render(fmt.Sprintf("  (attempt %d)", r.Attempt))}
	rows = append(rows, m.markdownRows(v.Summary, 0)...)
	if len(v.Findings) == 0 {
		return rows
	}
	rows = append(rows, "", sectionTitle("Findings")+mutedStyle.Render(fmt.Sprintf("  (%d)", len(v.Findings))))
	for i, f := range v.Findings {
		if i > 0 {
			rows = append(rows, "") // findings are blocks apart
		}
		rows = append(rows, "  "+boldStyle.Render(findingPlace(f)))
		rows = append(rows, m.markdownRows(f.Summary, 2)...)
	}
	return rows
}

func findingPlace(f protocol.Finding) string {
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d", safe(f.Path), f.Line)
	}
	return safe(f.Path)
}

// markdownRows lays out an agent's text the way messages are: the same
// Markdown subset, controls escaped, wrapped to the panel's width under a
// further indent.
func (m *Model) markdownRows(text string, indent int) []string {
	return m.formattedRows(lineBuilder{id: "review"}, text, indent)
}

// formattedRows lays out text with b under a further indent.
func (m *Model) formattedRows(b lineBuilder, text string, indent int) []string {
	b.formatBody(text, max(minWidth/2, m.width-indent))
	pad := strings.Repeat(" ", indent)
	rows := make([]string, 0, len(b.lines))
	for _, l := range b.lines {
		rows = append(rows, pad+l.text)
	}
	return rows
}

// publicationRows are what the forge recorded, kept apart from the verdict:
// a verdict to approve may be recorded as a comment.
func publicationRows(p protocol.Publication) []string {
	row := "  " + safe(p.State)
	if p.Reason != "" {
		row += " (" + safe(p.Reason) + ")"
	}
	rows := []string{"", sectionTitle("Publication"), row}
	if rc := p.Receipt; rc != nil {
		rows = append(rows, fmt.Sprintf("  recorded as %s by %s on %s", safe(recordedAs(*rc)), safe(rc.Identity), safe(reviewShort(rc.Revision))))
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

// pendingRows are a request the broker hasn't made a review of: still being
// requested, its answer lost and resent, or refused with the reason.
func (m *Model) pendingRows(p pendingReview) []string {
	rows := append([]string{"", sectionTitle("Target")}, targetRows(p.spec)...)
	switch {
	case p.refused != "":
		rows = append(rows, "  review request refused: "+p.refused, mutedStyle.Render("  the command is back on the line: fix it, then Enter"))
	case m.reviews.lost() || m.reviews.unknown:
		rows = append(rows, "  outcome unknown: the request "+safe(p.id)+" is resent unchanged once reconnected")
	default:
		rows = append(rows, "  requesting a review from "+safe(p.spec.Reviewer)+"…", mutedStyle.Render("  request "+safe(p.id)))
	}
	return rows
}
