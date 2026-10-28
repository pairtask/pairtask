package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
	"strings"
	"testing"
)

func TestReviewUXDetailRendersMarkdownAndDistinctPublication(t *testing.T) {
	h := reviewChatHarness(t)
	h.input(tea.WindowSizeMsg{Width: 100, Height: 70})
	row := reviewChatCompleted("review-one")
	row.Verdict.Summary = "**Security review**\n\nFirst paragraph.\n\nSecond paragraph."
	row.Verdict.Findings = []protocol.Finding{{Path: "internal/connsel/aa.go", Line: 85, Summary: "**P2** Sanitize credential errors.\n\nKeep secrets out of logs."}}
	v2Command(h, "/review status review-one")
	req := h.take(protocol.TypeOperatorReview)
	reviewChatSettle(h, req, row)
	view := ansi.Strip(h.m.View().Content)
	for _, want := range []string{"Target", "Verdict", "Findings", "Publication", "Security review", "First paragraph.", "Second paragraph.", "internal/connsel/aa.go:85", "Keep secrets out of logs.", "approve", "comment", "review-account"} {
		v3Contains(t, view, want)
	}
	reviewUXSectionSpacing(t, view)
	for _, raw := range []string{"**Security review**", "**P2**"} {
		if strings.Contains(view, raw) {
			t.Errorf("Markdown left raw: %q", raw)
		}
	}
	for _, width := range []int{80, 40, 24} {
		h.input(tea.WindowSizeMsg{Width: width, Height: 70})
		reviewUXWidth(t, h.m.View().Content, width)
	}
}

func reviewUXSectionSpacing(t *testing.T, view string) {
	t.Helper()
	previous := "not a blank line"
	for _, line := range strings.Split(view, "\n") {
		label := strings.TrimSpace(line)
		if reviewUXSection(label) && previous != "" {
			t.Errorf("section has no separating blank line: %q", label)
		}
		previous = label
	}
}

func reviewUXSection(label string) bool {
	for _, name := range []string{"Verdict", "Findings", "Publication"} {
		if strings.HasPrefix(label, name) {
			return true
		}
	}
	return false
}

func reviewUXWidth(t *testing.T, view string, width int) {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("width %d rendered %d columns: %q", width, got, line)
		}
	}
}
