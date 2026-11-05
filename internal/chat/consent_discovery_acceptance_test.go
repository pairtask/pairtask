package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func consentDiscoveryAnnounce(h *chatHarness, page protocol.Message) {
	h.t.Helper()
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventConsentChanged, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(h.t, "counting is read only", req.ContactOp, protocol.ContactOpConsentList)
	h.reply(req, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
}
func TestConsentDiscoveryAcceptanceNeedsYouAndPairStatusExposeCountsOnly(t *testing.T) {
	h := consentInputHarness(t)
	h.input(tea.PasteMsg{Content: "draft remains private"})
	consentDiscoveryAnnounce(h, consentInputPage())
	reviewPendingOpen(h, protocol.ReviewAwaitingPage{})
	view := ansi.Strip(h.m.View().Content)
	for _, want := range []string{"Needs you", "1 waiting", "/pair consent contact:"} {
		v3Contains(t, view, want)
	}
	if strings.Contains(view, consentFixtureText) {
		t.Fatal("Needs you exposed held request body")
	}
	h.input(chatKey(tea.KeyEscape, 0))
	v2Command(h, "/pair status")
	req := pairingChatRequest(h, "status")
	h.reply(req, contactChatReply())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view = ansi.Strip(h.m.View().Content)
	for _, want := range []string{"Waiting for you", "held for your decision", "/pair consent contact:"} {
		v3Contains(t, view, want)
	}
	chatEqual(t, "listing preserves draft", h.m.State().Draft, "draft remains private")
	consentInputNoWrites(t, h)
}
func TestConsentDiscoveryAcceptanceCompletionKeepsFrozenPairMenu(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{"pair", []string{"open", "status", "cancel", "accept", "reject", "redeem", "settle"}},
		{"pair consent ", []string{"on", "off", "reset"}},
		{"pair consent reset unknown alpha ", []string{"message", "review", "publish"}},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			h := consentInputHarness(t)
			if tc.line == "pair" {
				v2Command(h, "/pair")
			} else {
				commandCompleteLine(h, tc.line)
			}
			commandCompleteItems(t, h, tc.want)
			consentInputNoWrites(t, h)
		})
	}
}
func TestConsentDiscoveryAcceptanceAmbiguousContactCannotSelectOrMutate(t *testing.T) {
	for _, command := range []string{"contact:authenticate", "on authenticate", "reset authenticate alpha message"} {
		t.Run(command, func(t *testing.T) {
			h := consentInputHarness(t)
			page := consentInputPage()
			other := page.ConsentPage.Items[0]
			other.ID, other.Contact = "second-held-request", "authenticated-key-other"
			page.ConsentPage.Items = append(page.ConsentPage.Items, other)
			page.ConsentPage.Total = 2
			consentDiscoveryAnnounce(h, page)
			v2Command(h, "/pair consent "+command)
			v3Contains(t, h.m.State().Status, "names 2 paired workspaces")
			consentInputNoWrites(t, h)
		})
	}
}
func TestConsentDiscoveryAcceptancePagedCountsRemainReadOnlyAndComplete(t *testing.T) {
	h := consentInputHarness(t)
	page := consentInputPage()
	page.ConsentPage.More, page.ConsentPage.Next, page.ConsentPage.Total = true, 41, 2
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventConsentChanged, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	first := h.take(protocol.TypeOperatorContact)
	h.reply(first, page)
	next := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "pagination remains list read", next.ContactOp, protocol.ContactOpConsentList)
	chatEqual(t, "pagination cursor", next.ContactConsentBefore, int64(41))
	page.ConsentPage.More, page.ConsentPage.Next = false, 0
	page.ConsentPage.Items[0].ID = "second-page-request"
	h.reply(next, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, ansi.Strip(h.m.View().Content), "2 held for you")
	if h.m.State().Panel != "" {
		t.Fatal("count pagination opened a panel")
	}
	consentInputNoWrites(t, h)
}

func TestConsentDiscoveryAcceptancePartialResetDisclosesUnavailableStateWithoutWriting(t *testing.T) {
	h := consentInputHarness(t)
	consentDiscoveryAnnounce(h, consentInputPage())
	v2Command(h, "/pair consent reset authenticated-key-293 alpha")
	view := ansi.Strip(h.m.View().Content)
	unknown := strings.Contains(view, "unknown") || strings.Contains(view, "unavailable")
	if !unknown {
		t.Fatal("reset help presented scope state as known without a state read")
	}
	v3Contains(t, view, "paired before consent prompts?")
	for _, operation := range []string{"message", "review", "publish"} {
		v3Contains(t, view, "/pair consent reset authenticate alpha "+operation)
	}
	consentInputNoWrites(t, h)
}
