package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
)

func remoteAddressSquare(t *testing.T) (*chatHarness, string) {
	t.Helper()
	h, key := pairedMentionsChat(t, true)
	v2Command(h, "/chat all")
	chatEqual(t, "Square selected", h.m.State().SelectedAll, true)
	return h, key
}

func remoteAddressContacts(h *chatHarness, rows ...protocol.PairingContactView) {
	h.t.Helper()
	v2Command(h, "/pair list")
	h.reply(pairingChatRequest(h, "list"), contactChatReply(rows...))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyEscape, 0))
}

func remoteAddressHeld(t *testing.T, h *chatHarness, draft string) string {
	t.Helper()
	v2Type(h, draft)
	h.input(chatKey(tea.KeyEscape, 0)) // close completion; Enter is a send attempt
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "remote qualification never sends locally or remotely", h.m.State().InFlight, 0)
	chatEqual(t, "refusal preserves exact draft", h.m.State().Draft, draft)
	chatEqual(t, "refusal preserves selected conversation", h.m.State().SelectedAll, true)
	return strings.ToLower(h.m.State().Status + " " + h.m.State().Hint)
}

func remoteAddressTabGuidance(t *testing.T, text string) {
	t.Helper()
	if !strings.Contains(text, "tab") && !strings.Contains(text, "⇄") {
		t.Fatalf("remote qualification lacks paired-tab guidance: %s", text)
	}
	if strings.Contains(text, "/to") {
		t.Fatalf("remote qualification misleadingly recommends local literal addressing: %s", text)
	}
}
