package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"encoding/json"
	"github.com/pairtask/pairtask/internal/protocol"
	"testing"
)

func TestReviewUXTypedCommandAcceptsPastedGitHubArguments(t *testing.T) {
	for _, link := range []string{"3tio/3t.interceptor#323", "https://github.com/3tio/3t.interceptor/pull/323", "https://www.github.com/3tio/3t.interceptor/pull/323/files?diff=split#diff-example"} {
		t.Run(link, func(t *testing.T) {
			h := reviewChatHarness(t)
			v2Type(h, "/review --no-formal ")
			h.input(tea.PasteMsg{Content: link})
			v2Type(h, " reviewer security")
			h.input(chatKey(tea.KeyEnter, 0))
			if h.m.State().InFlight == 0 {
				t.Fatalf("pasted argument did not run: %s", h.m.State().Status)
			}
			req := h.take(protocol.TypeOperatorReview)
			chatEqual(t, "repository", req.ReviewSpec.Repository, "github.com/3tio/3t.interceptor")
			chatEqual(t, "change", req.ReviewSpec.Change, "323")
			chatEqual(t, "scope", req.ReviewSpec.Scope, "security")
			if req.ReviewSpec.Policy == nil {
				t.Fatal("missing explicit comment policy")
			}
			chatEqual(t, "comment policy", *req.ReviewSpec.Policy, protocol.ReviewPolicy{Publish: true})
			chatEqual(t, "no message intent", h.m.State().Intent, nil)
		})
	}
}

func TestReviewUXPastedCommandNameCannotExecute(t *testing.T) {
	for _, prefix := range []string{"", "rev"} {
		t.Run(prefix, func(t *testing.T) {
			h := reviewChatHarness(t)
			h.input(chatKey('k', tea.ModCtrl))
			v2Type(h, prefix)
			suffix := "review pairtask/pairtask#201 reviewer"
			if prefix != "" {
				suffix = "iew pairtask/pairtask#201 reviewer"
			}
			h.input(tea.PasteMsg{Content: suffix})
			h.input(chatKey(tea.KeyEnter, 0))
			chatEqual(t, "no request", h.m.State().InFlight, 0)
			chatEqual(t, "no send", h.m.State().Intent, nil)
			v3Contains(t, h.m.State().Status, "pasted")
		})
	}
}

func TestReviewUXPasteIntoTypedNameRevokesTrust(t *testing.T) {
	h := reviewChatHarness(t)
	v2Type(h, "/review pairtask/pairtask#201 reviewer")
	h.input(chatKey(tea.KeyHome, 0))
	h.input(chatKey(tea.KeyDelete, 0))
	h.input(tea.PasteMsg{Content: "r"})
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "pasted replacement cannot execute", h.m.State().InFlight, 0)
	chatEqual(t, "no send", h.m.State().Intent, nil)
	v3Contains(t, h.m.State().Status, "pasted")
}

func TestReviewUXRecalledCommandRequiresExplicitLiteralSend(t *testing.T) {
	entry := recallSent("old-command", 1, protocol.Broadcast)
	entry.Message.Payload = json.RawMessage(`{"text":"/review --no-formal 3tio/3t.interceptor#323 reviewer security"}`)
	h := recallHarness(t, entry)
	h.input(chatKey(tea.KeyUp, 0))
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "first Enter does not send", h.m.State().Intent, nil)
	chatEqual(t, "first Enter does not run", h.m.State().InFlight, 0)
	v3Contains(t, h.m.State().Status, "Enter again")
	v2Type(h, " updated")
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "edit re-arms guard", h.m.State().Intent, nil)
	sent := v2Send(h)
	chatEqual(t, "literal text preserved", v2Body(t, sent), "/review --no-formal 3tio/3t.interceptor#323 reviewer security updated")
}

func TestReviewUXPastedSlashCommandIsNotSilentlySent(t *testing.T) {
	h := reviewChatHarness(t)
	text := "/review --no-formal https://github.com/3tio/3t.interceptor/pull/323 reviewer security"
	h.input(tea.PasteMsg{Content: text})
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "paste does not become command", h.m.State().InFlight, 0)
	chatEqual(t, "first Enter does not send", h.m.State().Intent, nil)
	chatEqual(t, "draft preserved", h.m.State().Draft, text)
	v3Contains(t, h.m.State().Status, "Enter again")
}
