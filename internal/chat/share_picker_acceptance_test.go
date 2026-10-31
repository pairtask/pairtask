package chat_test

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func sharePickerHarness(t *testing.T, agents ...string) (*chatHarness, string) {
	t.Helper()
	key := "sha256:" + strings.Repeat("a", 64)
	h := contactChatHarness(t, contactChatRow(key, "Office", true))
	h.input(chatKey(tea.KeyTab, 0))
	req := h.take(protocol.TypeOperatorContact)
	page := contactMessagesPage(key)
	page.ContactPage.Directory = []string{"remote-only"}
	h.reply(req, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	for _, agent := range agents {
		revision := h.m.State().Revision + 1
		h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
			Kind: protocol.EventPresenceChanged, Watermark: protocol.Watermark{Epoch: "epoch", Revision: revision},
			AgentID: agent, SessionID: "session-" + agent, State: "connected",
		}}
		h.await(func() bool { return h.m.State().Revision == revision })
	}
	return h, key
}

func TestSharePickerAcceptanceBareShareOffersLocalAgentsAndRequiresSubmit(t *testing.T) {
	h, key := sharePickerHarness(t, "bob", "alice", "id with spaces")
	v2Type(h, "keep my draft")
	v2Command(h, "/pair share")
	commandCompleteItems(t, h, []string{"alice", "bob"})
	chatEqual(t, "opening picker changes no grants", h.m.State().InFlight, 0)
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "Office")
	v3Contains(t, strings.ToLower(view), "local")
	h.input(chatKey(tea.KeyTab, 0))
	chatEqual(t, "Tab fills command", h.m.State().CommandLine.Text, "pair share alice ")
	chatEqual(t, "selection changes no grants", h.m.State().InFlight, 0)
	chatEqual(t, "message draft retained", h.m.State().Draft, "keep my draft")
	h.input(chatKey(tea.KeyEnter, 0))
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "share operation", req.ContactOp, protocol.ContactOpShare)
	chatEqual(t, "full selected workspace identity", req.ContactKey, key)
	if !slices.Equal(req.ContactAgents, []string{"alice"}) {
		t.Fatalf("wrong agents shared: %q", req.ContactAgents)
	}
}

func TestSharePickerAcceptanceMentionCompletionUsesLocalDirectory(t *testing.T) {
	h, _ := sharePickerHarness(t, "bob", "alice")
	commandCompleteLine(h, "pair share @")
	commandCompleteItems(t, h, []string{"@alice", "@bob"})
	chatEqual(t, "completion changes no grants", h.m.State().InFlight, 0)
}

func TestSharePickerAcceptanceLiteralMentionNameKeepsItsIdentity(t *testing.T) {
	h, _ := sharePickerHarness(t, "@literal")
	v2Command(h, "/pair share")
	commandCompleteItems(t, h, []string{"@@literal"})
	h.input(chatKey(tea.KeyEnter, 0))
	req := h.take(protocol.TypeOperatorContact)
	if !slices.Equal(req.ContactAgents, []string{"@literal"}) {
		t.Fatalf("picker changed the selected agent identity: %q", req.ContactAgents)
	}
}

func TestSharePickerAcceptanceNormalizesExactlyOneMentionPrefix(t *testing.T) {
	for _, verb := range []string{"share", "unshare"} {
		t.Run(verb, func(t *testing.T) {
			h, key := sharePickerHarness(t, "alice", "@literal")
			v2Command(h, "/pair "+verb+" @alice @@literal")
			req := h.take(protocol.TypeOperatorContact)
			chatEqual(t, "operation", req.ContactOp, verb)
			chatEqual(t, "workspace", req.ContactKey, key)
			if !slices.Equal(req.ContactAgents, []string{"alice", "@literal"}) {
				t.Fatalf("mention normalization changed identities: %q", req.ContactAgents)
			}
		})
	}
}

func TestSharePickerAcceptanceExplicitWildcardRemainsExplicit(t *testing.T) {
	h, key := sharePickerHarness(t, "alice")
	v2Command(h, "/pair share *")
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "workspace", req.ContactKey, key)
	if !slices.Equal(req.ContactAgents, []string{"*"}) {
		t.Fatalf("explicit wildcard changed: %q", req.ContactAgents)
	}
}

func TestSharePickerAcceptanceNoLocalAgentsDoesNotOfferRemoteAgents(t *testing.T) {
	h, _ := sharePickerHarness(t)
	v2Command(h, "/pair share")
	chatEqual(t, "empty local directory changes no grants", h.m.State().InFlight, 0)
	if p := h.m.State().Picker; p != nil && len(p.Items) != 0 {
		t.Fatalf("remote directory became local share candidates: %q", p.Items)
	}
	v3Contains(t, strings.ToLower(h.m.State().Status), "no local agents")
}
