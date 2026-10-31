package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestAgentWorkspacePickerAcceptanceLabelDoesNotChangeAddress(t *testing.T) {
	h, key := sharePickerHarness(t, "remote-only")
	v2Type(h, "@")
	contactRecipientItems(t, h, "remote-only")
	v3Contains(t, ansi.Strip(h.m.View().Content), "@remote-only@Office")
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "canonical insertion", h.m.State().Draft, "@remote-only ")
	v2Type(h, "hello")
	h.input(chatKey(tea.KeyEnter, 0))
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "contact identity", req.ContactKey, key)
	chatEqual(t, "canonical recipient", strings.Join(req.ContactTo, ","), "remote-only")
	chatEqual(t, "body excludes display suffix", req.ContactText, "hello")
}

func TestAgentWorkspacePickerAcceptanceLocalNamesRemainBare(t *testing.T) {
	h, _ := sharePickerHarness(t, "local-agent")
	h.input(chatKey(tea.KeyTab, tea.ModShift))
	v2Type(h, "@local")
	v3Contains(t, ansi.Strip(h.m.View().Content), "@local-agent")
	if strings.Contains(ansi.Strip(h.m.View().Content), "@local-agent@Office") {
		t.Fatal("local picker inherited remote workspace")
	}
}
