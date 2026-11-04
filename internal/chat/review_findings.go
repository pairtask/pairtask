package chat

import (
	"fmt"
	"strconv"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Findings a paired request's verdict sent back (#303): the reviewer's
// side's words, shown escaped, and only by a broker that keeps them.

// cardFindings is how many findings a card shows.
const cardFindings = 3

// findingsShown: the broker keeps findings, and these have some.
func (m *Model) findingsShown(f *protocol.AskFindings) bool {
	return f != nil && f.Total > 0 && m.session.HasCapability(protocol.CapabilityAskFindings)
}

// findingsCount is an asked row's note of its findings, "" for none.
func (m *Model) findingsCount(f *protocol.AskFindings) string {
	if !m.findingsShown(f) {
		return ""
	}
	if f.Total == 1 {
		return " · 1 finding"
	}
	return fmt.Sprintf(" · %d findings", f.Total)
}

// findingsSteps are a card's lines for its findings: the first few, then
// how many more were received and where to read them, apart from those
// kept on their side.
func (m *Model) findingsSteps(it *protocol.ReviewAskedItem) []string {
	f := it.Findings
	if !m.findingsShown(f) {
		return nil
	}
	steps := []string{fmt.Sprintf("Findings (%d):", f.Total)}
	for _, item := range f.Items[:min(cardFindings, len(f.Items))] {
		steps = append(steps, "  "+findingLine(item))
	}
	if more := f.Received - min(cardFindings, len(f.Items)); more > 0 {
		ref := m.sections.ref(askedKey(*it)) // the request's own ref: the same whatever the list's paging
		steps = append(steps, mutedStyle.Render(fmt.Sprintf("  +%d more · /reviews findings %s", more, ref)))
	}
	if kept := f.Total - f.Received; kept > 0 {
		steps = append(steps, mutedStyle.Render(fmt.Sprintf("  %d kept on their side", kept)))
	}
	return steps
}

// findingLine is one finding, escaped: where, then what.
func findingLine(f protocol.AskFinding) string {
	where := safe(f.Path)
	if f.Line > 0 {
		where += ":" + strconv.Itoa(f.Line)
	}
	line := where + ": " + safe(f.Summary)
	if f.Truncated {
		line += "…"
	}
	return line
}
