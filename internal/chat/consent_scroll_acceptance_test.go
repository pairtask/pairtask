package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"strings"
	"testing"
)

func TestConsentScrollAcceptanceFullBodyCanBeInspectedWithoutEditingDraft(t *testing.T) {
	h := consentInputHarness(t)
	h.input(tea.PasteMsg{Content: "preserved under long preview"})
	before := h.m.State()
	page := consentInputPage()
	page.ConsentPage.Items[0].Text = consentFixtureText + "\n" + strings.Repeat("pending body line\n", 80) + "LAST PENDING BODY LINE"
	consentInputOpen(t, h, page)
	if strings.Contains(ansi.Strip(h.m.View().Content), "LAST PENDING BODY LINE") {
		t.Fatal("long-preview fixture unexpectedly fits on one screen")
	}
	for i := 0; i < 8; i++ {
		h.input(chatKey(tea.KeyPgDown, 0))
	}
	if !strings.Contains(ansi.Strip(h.m.View().Content), "LAST PENDING BODY LINE") {
		t.Fatal("consent overlay cannot scroll to show complete immutable body")
	}
	consentInputNoWrites(t, h)
	after := h.m.State()
	consentAssertScrollState(t, before, after)
	h.input(chatKey(tea.KeyEscape, 0))
	if h.m.State().Draft != before.Draft {
		t.Fatal("closing scrolled preview lost draft")
	}
}

func consentAssertScrollState(t *testing.T, before, after chat.StateView) {
	t.Helper()
	editorChanged := after.Draft != before.Draft || after.Pasted != before.Pasted
	conversationChanged := after.Selected != before.Selected || after.ScrollOffset != before.ScrollOffset
	if editorChanged || conversationChanged {
		t.Fatal("preview scrolling escaped into composer or conversation")
	}
}
