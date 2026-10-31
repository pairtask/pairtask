package chat_test

import (
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
)

func contactRecipientItems(t *testing.T, h *chatHarness, want ...string) {
	t.Helper()
	p := h.m.State().Picker
	if len(want) == 0 {
		chatEqual(t, "no recipients offered", p, nil)
		return
	}
	if p == nil {
		t.Fatal("shared recipient picker missing")
	}
	chatEqual(t, "recipient picker kind", p.Kind, "agent")
	if !slices.Equal(p.Items, want) {
		t.Fatalf("recipients = %v, want %v", p.Items, want)
	}
}

func contactRecipientRefresh(t *testing.T, h *chatHarness, key string, agents ...string) {
	t.Helper()
	revision := h.m.State().Revision + 1
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
		Kind: protocol.EventContactMessagesChanged, ContactKey: key,
		Watermark: protocol.Watermark{Epoch: "epoch", Revision: revision},
	}}
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "refresh only reads", req.ContactOp, protocol.ContactOpList)
	page := contactMessagesPage(key)
	page.ContactPage.Directory = agents
	h.reply(req, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func TestContactRecipientPickerAcceptanceOnlyRemoteSharedAgents(t *testing.T) {
	h, key := sharePickerHarness(t, "codex-1", "claude-1")
	v2Type(h, "@")
	contactRecipientItems(t, h, "remote-only")
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "completion sends nothing", h.m.State().InFlight, 0)
	v2Type(h, "test ping")
	h.input(chatKey(tea.KeyEnter, 0))
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "remote workspace retained", req.ContactKey, key)
	if !slices.Equal(req.ContactTo, []string{"remote-only"}) {
		t.Fatalf("wrong remote recipient: %v", req.ContactTo)
	}
	chatEqual(t, "message payload", req.ContactText, "test ping")
}

func TestContactRecipientPickerAcceptanceOpenPickerTracksSharing(t *testing.T) {
	h, key := sharePickerHarness(t, "codex-1")
	v2Type(h, "@")
	contactRecipientItems(t, h, "remote-only")
	contactRecipientRefresh(t, h, key, "claude-1")
	contactRecipientItems(t, h, "claude-1")
	chatEqual(t, "refresh preserves typed draft", h.m.State().Draft, "@")
	contactRecipientRefresh(t, h, key)
	contactRecipientItems(t, h)
	chatEqual(t, "revocation doesn't send", h.m.State().InFlight, 0)
}

func TestContactRecipientPickerAcceptancePrefixAndAlreadyNamed(t *testing.T) {
	h, key := sharePickerHarness(t, "local-only")
	contactRecipientRefresh(t, h, key, "zeta", "claude-1", "codex-1")
	v2Type(h, "@claude-1 @co")
	contactRecipientItems(t, h, "codex-1")
	h.input(chatKey(tea.KeyBackspace, 0))
	h.input(chatKey(tea.KeyBackspace, 0))
	contactRecipientItems(t, h, "codex-1", "zeta")
	chatEqual(t, "choosing changes no sharing", h.m.State().InFlight, 0)
}

func TestContactRecipientPickerAcceptanceRefreshKeepsSelectedIdentity(t *testing.T) {
	h, key := sharePickerHarness(t)
	contactRecipientRefresh(t, h, key, "claude-1", "codex-1")
	v2Type(h, "@")
	h.input(chatKey(tea.KeyDown, 0))
	contactRecipientItems(t, h, "claude-1", "codex-1")
	chatEqual(t, "codex selected", h.m.State().Picker.Selected, 1)
	contactRecipientRefresh(t, h, key, "alpha", "claude-1", "codex-1")
	contactRecipientItems(t, h, "alpha", "claude-1", "codex-1")
	p := h.m.State().Picker
	chatEqual(t, "refresh retains selected agent", p.Items[p.Selected], "codex-1")
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "completion retains intended agent", h.m.State().Draft, "@codex-1 ")
	chatEqual(t, "completion sends nothing", h.m.State().InFlight, 0)
}
