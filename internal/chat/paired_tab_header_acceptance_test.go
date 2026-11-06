package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPairedTabHeaderAcceptanceUnfinishedKnownAndUnknownWordsAreEditing(t *testing.T) {
	for _, word := range []string{"remote-a", "rem"} {
		t.Run(word, func(t *testing.T) { h, _ := pairedTabHarness(t); v2Type(h, "@"+word); pairedTabEditing(t, h, word) })
	}
}
func TestPairedTabHeaderAcceptanceChoosingOrDelimiterCommitsPreview(t *testing.T) {
	for _, commit := range []string{"picker", "delimiter"} {
		t.Run(commit, func(t *testing.T) {
			h, _ := pairedTabHarness(t)
			v2Type(h, "@remote-a")
			if commit == "picker" {
				pairedTabPick(t, h, "remote-a@Office", "remote-a")
			} else {
				v2Type(h, " ")
			}
			v3Contains(t, h.m.State().Hint, "remote-a@Office")
			if strings.Contains(strings.ToLower(h.m.State().Hint), "editing") {
				t.Fatal("committed mention still described as editing")
			}
			v2Type(h, "body")
			h.input(chatKey(tea.KeyEnter, 0))
			req := h.take(protocol.TypeOperatorContact)
			chatEqual(t, "send keeps remote agent", strings.Join(req.ContactTo, ","), "remote-a")
			chatEqual(t, "send strips committed tag", req.ContactText, "body")
			pairedMentionsChatAccept(t, h, req)
		})
	}
}
func TestPairedTabHeaderAcceptanceCursorInsideMentionIsEditing(t *testing.T) {
	h, _ := pairedTabHarness(t)
	v2Type(h, "@remote-a body")
	h.input(chatKey(tea.KeyHome, 0))
	for range 3 {
		h.input(chatKey(tea.KeyRight, 0))
	}
	pairedTabEditing(t, h, "remote-a")
	chatEqual(t, "moving cursor keeps exact text", h.m.State().Draft, "@remote-a body")
}
func TestPairedTabHeaderAcceptancePasteKeepsExistingRoutingAndValidation(t *testing.T) {
	h, _ := pairedTabHarness(t)
	h.input(tea.PasteMsg{Content: "@remote-a literal text"})
	chatEqual(t, "paste never opens a recipient picker", h.m.State().Picker == nil, true)
	v3Contains(t, h.m.State().Hint, "remote-a@Office")
	h.input(chatKey(tea.KeyEnter, 0)) // existing pasted-draft confirmation
	if h.m.State().InFlight == 0 {
		h.input(chatKey('y', tea.ModCtrl))
	}
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "pasted paired routing remains unchanged", strings.Join(req.ContactTo, ","), "remote-a")
	chatEqual(t, "pasted text uses existing parser", req.ContactText, "literal text")
	pairedMentionsChatAccept(t, h, req)
}
