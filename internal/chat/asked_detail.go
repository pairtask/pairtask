package chat

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Asked detail (#303): /reviews findings aN shows one request this side
// sent with every finding its verdict sent back, read from the broker, so
// they outlive the session's cards. /reviews open aN still goes to its ⇄
// tab.

const kindAskedDetail = "asked detail"

// askedDetail is the request shown, and its read: item nil while it reads.
type askedDetail struct {
	ref     sectionRef
	item    *protocol.ReviewAskedItem
	gone    bool   // a read that worked no longer has it
	failure string // the read was refused
}

// askedFindings runs /reviews findings aN: a request this side sent, read
// with every finding received, from a broker that keeps them.
func (m *Model) askedFindings(ref sectionRef) tea.Cmd {
	if !m.session.HasCapability(protocol.CapabilityAskFindings) {
		m.status = "this broker doesn't keep findings of requests sent; upgrade it"
		return nil
	}
	if _, ok := askedRef(m.sections.byRef[ref]); !ok {
		m.status = string(ref) + " is no request sent here: /reviews lists them as aN"
		return nil
	}
	return m.readAskedDetail(ref)
}

// askedRefs are the Asked section's refs, as shown.
func (m *Model) askedRefs() []string {
	var refs []string
	for _, it := range m.sections.asked.items {
		refs = append(refs, string(m.sections.ref(askedKey(it))))
	}
	return refs
}

// askedRef is the request an asked identity names.
func askedRef(identity itemKey) (protocol.ReviewAskRef, bool) {
	rest, ok := strings.CutPrefix(string(identity), "a")
	contact, askID, cut := strings.Cut(rest, "\x00")
	return protocol.ReviewAskRef{Contact: contact, AskID: askID}, ok && cut
}

// readAskedDetail shows the request and reads it with all its findings,
// fenced: a later read, or a lost link, drops this one's answer.
func (m *Model) readAskedDetail(ref sectionRef) tea.Cmd {
	asked, _ := askedRef(m.sections.byRef[ref])
	m.sections.detailGen++
	m.sections.detail = &askedDetail{ref: ref}
	m.reviews.selected, m.reviews.detail = "", nil
	m.panel = &panel{kind: panelReviews}
	msg := protocol.Message{Type: protocol.TypeOperatorReview, ReviewOp: protocol.ReviewOpAskedDetail, ReviewAskRefs: []protocol.ReviewAskRef{asked}}
	return m.request(kindAskedDetail, msg, strconv.FormatUint(m.sections.detailGen, 10))
}

// askedDetailResult takes the read's answer, if it is still the one asked.
func (m *Model) askedDetailResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	d := m.sections.detail
	if d == nil || req.ids[0] != strconv.FormatUint(m.sections.detailGen, 10) {
		return nil
	}
	switch {
	case resp.Type != protocol.TypeOperatorReviewResult || resp.ReviewAsked == nil:
		d.failure = refusalReason(resp)
	case len(resp.ReviewAsked.Items) == 0:
		d.gone = true
	default:
		it := resp.ReviewAsked.Items[0]
		d.item = &it
	}
	return nil
}

// askedDetailLost drops a read the link lost.
func (m *Model) askedDetailLost() {
	m.sections.detailGen++
	if d := m.sections.detail; d != nil && d.item == nil {
		m.sections.detail = nil
	}
}

// askedDetailRows are the request shown and every finding received.
func (m *Model) askedDetailRows(d *askedDetail) []string {
	head := "  " + string(d.ref) + mutedStyle.Render("  (/reviews open "+string(d.ref)+": its ⇄ tab · /reviews: the list)")
	switch {
	case d.failure != "":
		return []string{head, "  " + errStyle.Render("couldn't read it: "+safe(d.failure))}
	case d.gone:
		return []string{head, mutedStyle.Render("  no longer kept here")}
	case d.item == nil:
		return []string{head, mutedStyle.Render("  reading…")}
	}
	it := d.item
	ws := workspaceLabel(safe(m.contactTitle(contactKey(it.Contact))))
	rows := []string{head, "  " + safe(it.Reviewer) + "@" + string(ws) + "  " + theirState(it.State, it.Reviewer, ws) + verdictWords(*it)}
	if it.Reason != "" {
		rows = append(rows, "  reason: "+safe(it.Reason))
	}
	rows = append(rows, mutedStyle.Render("  "+theirReviewID(it.ReviewID)))
	return append(rows, m.receivedFindingRows(it.Findings)...)
}

// receivedFindingRows are every finding received, then how many stayed on
// their side.
func (m *Model) receivedFindingRows(f *protocol.AskFindings) []string {
	if f == nil || f.Total == 0 {
		return nil
	}
	rows := []string{"", sectionTitle("Findings") + mutedStyle.Render(fmt.Sprintf("  (%d received of %d)", f.Received, f.Total))}
	for _, it := range f.Items {
		rows = append(rows, "  "+findingLine(it))
	}
	if kept := f.Total - f.Received; kept > 0 {
		rows = append(rows, mutedStyle.Render(fmt.Sprintf("  %d kept on their side", kept)))
	}
	return rows
}
