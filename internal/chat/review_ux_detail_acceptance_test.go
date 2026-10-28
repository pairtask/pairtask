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
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		label := strings.TrimSpace(line)
		if strings.HasPrefix(label, "Verdict") || strings.HasPrefix(label, "Findings") || label == "Publication" {
			if i == 0 || strings.TrimSpace(lines[i-1]) != "" {
				t.Errorf("section has no separating blank line: %q", label)
			}
		}
	}
	for _, raw := range []string{"**Security review**", "**P2**"} {
		if strings.Contains(view, raw) {
			t.Errorf("Markdown left raw: %q", raw)
		}
	}
	for _, width := range []int{80, 40, 24} {
		h.input(tea.WindowSizeMsg{Width: width, Height: 70})
		for _, line := range strings.Split(h.m.View().Content, "\n") {
			if got := ansi.StringWidth(line); got > width {
				t.Fatalf("width %d rendered %d columns: %q", width, got, line)
			}
		}
	}
}
