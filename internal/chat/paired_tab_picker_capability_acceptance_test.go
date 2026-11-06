package chat_test

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
)

func pairedTabCapabilityHarness(t *testing.T, capable bool) (*chatHarness, string) {
	t.Helper()
	caps := slices.Clone(protocol.SessionCapabilities)
	if !capable {
		caps = slices.DeleteFunc(caps, func(c string) bool { return c == protocol.CapabilityPairedMentions })
	}
	h := pairingChatCapabilities(t, caps)
	key := "sha256:" + strings.Repeat("a", 64)
	v2Command(h, "/pair list")
	h.reply(pairingChatRequest(h, "list"), contactChatReply(contactChatRow(key, "Office", true)))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyEscape, 0))
	h.input(chatKey(tea.KeyTab, 0))
	h.reply(h.take(protocol.TypeOperatorContact), contactMessagesPage(key))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h, key
}

func TestPairedTabPickerAcceptanceOperatorsWithoutAgentsOrShares(t *testing.T) {
	h, _ := pairedTabCapabilityHarness(t, true)
	v2Type(h, "@")
	contactRecipientItems(t, h, "operator@Office", "operator")
	chatEqual(t, "opening operators never sends", h.m.State().InFlight, 0)
}

func TestPairedTabPickerAcceptanceLegacyAgentVersionDoesNotHideChoices(t *testing.T) {
	h, _ := sharePickerHarness(t, "legacy-worker") // presence ProtocolVersion is zero
	v2Type(h, "@")
	contactRecipientItems(t, h, "remote-only", "operator@Office", "legacy-worker@here", "operator")
	chatEqual(t, "offering a legacy agent never sends", h.m.State().InFlight, 0)
}

func TestPairedTabPickerAcceptanceCapabilityAbsentKeepsLegacyDiscovery(t *testing.T) {
	h, key := pairedTabCapabilityHarness(t, false)
	revision := h.m.State().Revision + 1
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
		Kind: protocol.EventPresenceChanged, AgentID: "local-worker", SessionID: "local-session", State: "connected",
		ProtocolVersion: protocol.Version, Watermark: protocol.Watermark{Epoch: "epoch", Revision: revision},
	}}
	h.await(func() bool { return h.m.State().Revision == revision })
	v2Type(h, "@")
	contactRecipientItems(t, h)
	contactRecipientRefresh(t, h, key, "remote-only")
	h.input(chatKey(tea.KeyBackspace, 0))
	v2Type(h, "@")
	contactRecipientItems(t, h, "remote-only")
	h.input(chatKey(tea.KeyBackspace, 0))
	v2Type(h, "@op")
	contactRecipientItems(t, h)
	chatEqual(t, "legacy discovery sends nothing", h.m.State().InFlight, 0)
}
