package chat_test

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestAcceptKeyCompletionContactVerbAndTabDoesNotApprove(t *testing.T) {
	person := "sha256:" + strings.Repeat("c", 64)
	h, _ := contactKeyCommandHarness(t, person)
	commandCompleteLine(h, "pair accept-k")
	commandCompleteItems(t, h, []string{"accept-key"})
	h.input(chatKey(tea.KeyTab, 0))
	chatEqual(t, "verb completed", h.m.State().CommandLine.Text, "pair accept-key ")
	chatEqual(t, "Tab never approves", h.m.State().InFlight, 0)
	commandCompleteItems(t, h, []string{person})
}

func TestAcceptKeyCompletionFullFingerprintAndExplicitExecution(t *testing.T) {
	first := "sha256:" + strings.Repeat("c", 64)
	second := "sha256:" + strings.Repeat("d", 64)
	h, contact := contactKeyCommandHarness(t, first, second)
	commandCompleteLine(h, "pair accept-key sha256:d")
	commandCompleteItems(t, h, []string{second})
	h.input(chatKey(tea.KeyTab, 0))
	chatEqual(t, "full exact fingerprint", h.m.State().CommandLine.Text, "pair accept-key "+second+" ")
	chatEqual(t, "Tab never approves", h.m.State().InFlight, 0)
	chatEqual(t, "no second argument", h.m.State().Picker, nil)
	h.input(chatKey(tea.KeyEnter, 0))
	req := h.take(protocol.TypeOperatorContact)
	if req.ContactOp != protocol.ContactOpAcceptKey || req.ContactKey != contact || req.ContactText != second {
		t.Fatalf("completion approved wrong contact/key: %+v", req)
	}
}

func TestAcceptKeyCompletionNoSuggestionsOutsideContact(t *testing.T) {
	for _, line := range []string{"pair accept-k", "pair accept-key "} {
		h := commandCompleteHarness(t)
		commandCompleteLine(h, line)
		chatEqual(t, "contact-only suggestions", h.m.State().Picker, nil)
		chatEqual(t, "no request", h.m.State().InFlight, 0)
	}
}

func TestAcceptKeyCompletionOnlyPendingKeysAndNoExtraArguments(t *testing.T) {
	first := "sha256:" + strings.Repeat("c", 64)
	second := "sha256:" + strings.Repeat("d", 64)
	for _, tc := range []struct {
		line    string
		pending []string
		want    []string
	}{
		{"pair accept-key ", []string{first, second}, []string{first, second}},
		{"pair accept-key ", nil, nil},
		{"pair accept-key sha256:e", []string{first, second}, nil},
		{"pair accept-key " + first + " ", []string{first, second}, nil},
	} {
		t.Run(tc.line+strings.Join(tc.pending, ","), func(t *testing.T) {
			h, _ := contactKeyCommandHarness(t, tc.pending...)
			commandCompleteLine(h, tc.line)
			p := h.m.State().Picker
			if len(tc.want) == 0 {
				chatEqual(t, "no candidate", p, nil)
			} else if p == nil || !slices.Equal(p.Items, tc.want) {
				t.Fatalf("pending candidates = %+v, want %q", p, tc.want)
			}
			chatEqual(t, "suggestions never approve", h.m.State().InFlight, 0)
		})
	}
}
