package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
	"testing"
)

func TestReviewMentionAcceptanceReviewerAlias(t *testing.T) {
	for _, token := range []string{"codex-1", "@codex-1", "claude-1", "@claude-1", "@@literal"} {
		t.Run(token, func(t *testing.T) {
			h := reviewChatHarness(t)
			v2Command(h, "/review --no-publish 3tio/docs#123 "+token+" check @other references")
			req := h.take(protocol.TypeOperatorReview)
			want := token
			if token[0] == '@' {
				want = token[1:]
			}
			chatEqual(t, "canonical reviewer", req.ReviewSpec.Reviewer, want)
			chatEqual(t, "scope remains literal", req.ReviewSpec.Scope, "check @other references")
			chatEqual(t, "no publication", req.ReviewSpec.Policy.Publish, false)
			chatEqual(t, "review operation", req.ReviewOp, protocol.ReviewOpRequest)
		})
	}
}

func TestReviewMentionAcceptanceLoneAtCannotRequest(t *testing.T) {
	h := reviewChatHarness(t)
	v2Command(h, "/review --no-publish 3tio/docs#123 @")
	chatEqual(t, "no request for empty alias", h.m.State().InFlight, 0)
}

func TestReviewMentionAcceptanceReviewerCompletion(t *testing.T) {
	for _, tc := range []struct{ line, completed string }{
		{"review 3tio/docs#123 co", "review 3tio/docs#123 codex-1 "},
		{"review --no-publish 3tio/docs#123 @co", "review --no-publish 3tio/docs#123 @codex-1 "},
		{"review --no-formal --no-publish 3tio/docs#123 @co", "review --no-formal --no-publish 3tio/docs#123 @codex-1 "},
	} {
		t.Run(tc.line, func(t *testing.T) {
			h := commandCompleteHarness(t, "codex-1")
			commandCompleteLine(h, tc.line)
			h.input(chatKey(tea.KeyTab, 0))
			chatEqual(t, "completed reviewer", h.m.State().CommandLine.Text, tc.completed)
			chatEqual(t, "Tab does not submit", h.m.State().InFlight, 0)
			h.input(chatKey(tea.KeyEnter, 0))
			req := h.take(protocol.TypeOperatorReview)
			chatEqual(t, "reviewer after completion", req.ReviewSpec.Reviewer, "codex-1")
		})
	}
}

func TestReviewMentionAcceptanceScopeIsNotReviewerCompletion(t *testing.T) {
	h := commandCompleteHarness(t, "codex-1")
	line := "review 3tio/docs#123 codex-1 check @co"
	commandCompleteLine(h, line)
	h.input(chatKey(tea.KeyTab, 0))
	chatEqual(t, "scope literal", h.m.State().CommandLine.Text, line)
	chatEqual(t, "scope has no reviewer picker", h.m.State().Picker, nil)
}

func TestReviewMentionAcceptanceCompletionDoesNotChangeAgentIdentity(t *testing.T) {
	h := commandCompleteHarness(t, "@literal")
	commandCompleteLine(h, "review 3tio/docs#123 ")
	picker := h.m.State().Picker
	if picker == nil {
		t.Fatal("reviewer picker missing")
	}
	found := false
	for _, candidate := range picker.Items {
		if candidate == "@literal" {
			t.Fatal("completion strips literal agent @ prefix on submission")
		}
		if candidate == "id with spaces" {
			t.Fatal("completion inserts multiple words as one reviewer")
		}
		if candidate == "@@literal" {
			found = true
		}
	}
	if !found {
		t.Fatal("literal @ agent has no correctly escaped completion")
	}
}
