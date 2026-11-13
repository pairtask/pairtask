package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func signingAlertChat(t *testing.T, signing string, require bool) (*chatHarness, protocol.PairingContactView, string) {
	t.Helper()
	c := contactChatRow("sha256:"+strings.Repeat("a", 64), "Old peer", true)
	c.Signing = signing
	h := contactChatHarness(t, c)
	h.input(chatKey(tea.KeyTab, 0))
	req := h.take(protocol.TypeOperatorContact)
	page := contactMessagesPage(c.IdentityKey)
	page.ContactPage.RequireSeals = require
	h.reply(req, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h, c, ansi.Strip(h.m.View().Content)
}

func TestContactSigningAlertAcceptanceUnsupportedContactExplainsGrace(t *testing.T) {
	for _, require := range []bool{false, true} {
		h, _, view := signingAlertChat(t, protocol.ContactSigningUnsupported, require)
		for _, want := range []string{"┌ Old peer runs a version without signatures", "│ Their messages arrive unsigned and can't be verified", "Ask them to update pairtask", "└"} {
			v3Contains(t, view, want)
		}
		footer := "nothing of theirs is refused"
		if require {
			footer = "require-seals takes effect once they update"
		}
		v3Contains(t, view, footer)
		if strings.Count(view, "runs a version without signatures") != 1 {
			t.Fatalf("alert was duplicated in the contact tab: %q", view)
		}
		v2Command(h, "/cards clear")
		v3Contains(t, ansi.Strip(h.m.View().Content), "Old peer runs a version without signatures")
	}
}

func TestContactSigningAlertAcceptanceDowngradeDependsOnPolicy(t *testing.T) {
	for _, require := range []bool{false, true} {
		_, _, view := signingAlertChat(t, protocol.ContactSigningDowngraded, require)
		for _, want := range []string{"Old peer went back to a version without signatures", "They signed before", "unexpected"} {
			v3Contains(t, view, want)
		}
		footer := "their messages arrive unsigned"
		if require {
			footer = "their messages are refused while require-seals is on; /pair require-seals off"
		}
		v3Contains(t, view, footer)
	}
}

func TestContactSigningAlertAcceptanceStatusAndClearWhenUpgraded(t *testing.T) {
	for _, tc := range []struct{ signing, label string }{
		{protocol.ContactSigningUnsupported, "unsigned: their version can't sign"},
		{protocol.ContactSigningDowngraded, "went back to a version without signatures"},
	} {
		c := contactChatRow("sha256:"+strings.Repeat("b", 64), "Old peer", true)
		c.Signing = tc.signing
		_, view := pairSummaryView(t, c)
		v3Contains(t, view, tc.label)
	}
	_, _, view := signingAlertChat(t, "", true)
	if strings.Contains(view, "version without signatures") || strings.Contains(view, "takes effect once they update") {
		t.Fatalf("upgraded or unobserved contact still has legacy alert: %q", view)
	}
}

func TestContactSigningAlertAcceptanceRequireOnReplyExplainsGrace(t *testing.T) {
	h, c, _ := signingAlertChat(t, protocol.ContactSigningUnsupported, false)
	v2Command(h, "/pair require-seals on")
	req := h.take(protocol.TypeOperatorContact)
	if req.ContactOp != protocol.ContactOpRequireSeals || req.ContactKey != c.IdentityKey || req.ContactText != "on" {
		t.Fatalf("require-seals targeted wrong contact: %+v", req)
	}
	page := contactMessagesPage(c.IdentityKey)
	page.ContactPage.RequireSeals = true
	h.reply(req, page)
	refresh := h.take(protocol.TypeOperatorContact)
	h.reply(refresh, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, h.m.State().Status, "until then their messages arrive unsigned")
	v3Contains(t, ansi.Strip(h.m.View().Content), "require-seals takes effect once they update")
}

func TestContactSigningAlertAcceptanceFitsNarrowTabs(t *testing.T) {
	for _, signing := range []string{protocol.ContactSigningUnsupported, protocol.ContactSigningDowngraded} {
		t.Run(signing, func(t *testing.T) {
			h, _, _ := signingAlertChat(t, signing, true)
			for _, size := range []tea.WindowSizeMsg{{Width: 45, Height: 24}, {Width: 80, Height: 30}} {
				h.input(size)
				v2FrameFits(t, h, size.Width, size.Height)
			}
		})
	}
}
