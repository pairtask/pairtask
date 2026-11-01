package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func selfNameStatus(t *testing.T, name string) string {
	t.Helper()
	h := pairingChatHarness(t, true)
	h.input(tea.WindowSizeMsg{Width: 220, Height: 40})
	v2Command(h, "/pair status")
	req := pairingChatRequest(h, "status")
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorPairingResult, PairingResult: &protocol.PairingSnapshot{SelfIdentity: "sha256:" + strings.Repeat("a", 64), SelfName: name}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return ansi.Strip(h.m.View().Content)
}

func TestPairingSelfNameAcceptanceUnnamedHintCoversManagedAndUnmanaged(t *testing.T) {
	for _, name := range []string{"", "workspace"} {
		view := selfNameStatus(t, name)
		v3Contains(t, view, "pairtask pairing on --name NAME")
		v3Contains(t, view, "PAIRTASK_PAIRING_NAME")
	}
}

func TestPairingSelfNameAcceptanceNamedWorkspaceHasNoUnnamedHint(t *testing.T) {
	view := selfNameStatus(t, "My workspace")
	v3Contains(t, view, "My workspace")
	if strings.Contains(view, "no name:") {
		t.Fatal("named workspace still shown as unnamed")
	}
}
