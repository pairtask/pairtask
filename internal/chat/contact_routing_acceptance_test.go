package chat_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestContactRoutingAcceptanceAddressedDraftAndEdits(t *testing.T) {
	for _, edit := range []bool{false, true} {
		h, key := contactMessagesHarness(t)
		h.await(func() bool { return h.m.State().InFlight == 0 })
		v2Type(h, "@first @remote:agent/@name question")
		h.input(chatKey(tea.KeyEnter, 0))
		req := h.take(protocol.TypeOperatorContact)
		chatEqual(t, "routing operation", req.ContactOp, protocol.ContactOpSend)
		chatEqual(t, "frozen workspace", req.ContactKey, key)
		chatEqual(t, "address stripped from text", req.ContactText, "question")
		if !reflect.DeepEqual(req.ContactTo, []string{"first", "remote:agent/@name"}) {
			t.Fatalf("wrong targets: %q", req.ContactTo)
		}
		want := ""
		if edit {
			v2Type(h, " edited")
			want = h.m.State().Draft
		}
		h.reply(req, contactMessagesQueued(req))
		h.await(func() bool { return h.m.State().InFlight == 0 })
		chatEqual(t, "draft cleared only if unchanged", h.m.State().Draft, want)
	}
}

func TestContactRoutingAcceptanceInvalidAddressesKeepDraft(t *testing.T) {
	for _, draft := range []string{"@first", "@first @first question", "@operator question", "@* question"} {
		t.Run(draft, func(t *testing.T) {
			h, _ := contactMessagesHarness(t)
			h.await(func() bool { return h.m.State().InFlight == 0 })
			v2Type(h, draft)
			h.input(chatKey(tea.KeyEnter, 0))
			chatEqual(t, "invalid draft not sent", h.m.State().InFlight, 0)
			chatEqual(t, "draft kept", h.m.State().Draft, draft)
		})
	}
}

func TestContactRoutingAcceptanceMissingCapabilityCannotSendOrShare(t *testing.T) {
	caps := slices.DeleteFunc(slices.Clone(protocol.SessionCapabilities), func(c string) bool { return c == protocol.CapabilityContactRouting })
	h := pairingChatCapabilities(t, caps)
	key := "sha256:" + strings.Repeat("a", 64)
	v2Command(h, "/pair list")
	req := pairingChatRequest(h, "list")
	h.reply(req, contactChatReply(contactChatRow(key, "remote", true)))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyEscape, 0))
	h.input(chatKey(tea.KeyTab, 0))
	req = h.take(protocol.TypeOperatorContact)
	h.reply(req, contactMessagesPage(key))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Type(h, "@first question")
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "old broker send refused locally", h.m.State().InFlight, 0)
	chatEqual(t, "draft kept", h.m.State().Draft, "@first question")
	v3Contains(t, ansi.Strip(h.m.View().Content), "upgrade")
	v2Command(h, "/pair share first")
	chatEqual(t, "old broker share refused locally", h.m.State().InFlight, 0)
	v3Contains(t, ansi.Strip(h.m.View().Content), "upgrade")
}

func TestContactRoutingAcceptanceShareUsesSelectedWorkspaceAndShowsResult(t *testing.T) {
	for _, verb := range []string{"share", "unshare"} {
		t.Run(verb, func(t *testing.T) {
			h, key := contactMessagesHarness(t)
			h.await(func() bool { return h.m.State().InFlight == 0 })
			v2Command(h, "/pair "+verb+" first second")
			req := h.take(protocol.TypeOperatorContact)
			chatEqual(t, "grant operation", req.ContactOp, verb)
			chatEqual(t, "selected full contact key", req.ContactKey, key)
			if !reflect.DeepEqual(req.ContactAgents, []string{"first", "second"}) {
				t.Fatalf("wrong grant targets: %q", req.ContactAgents)
			}
			page := contactMessagesPage(key)
			page.ContactPage.Shared, page.ContactPage.Directory = []string{"remaining"}, []string{"remote-agent"}
			h.reply(req, page)
			h.await(func() bool { return h.m.State().InFlight == 0 })
			view := ansi.Strip(h.m.View().Content)
			v3Contains(t, view, "you share remaining")
			v3Contains(t, view, "they share remote-agent")
		})
	}
}

func TestContactRoutingAcceptanceRemoteNamesAreDisplayData(t *testing.T) {
	name := "evil\x1b[2J\rname\x07"
	h, _ := contactMessagesHarness(t, protocol.ContactMessageView{ID: "routed", Cursor: 1, Direction: "in", State: "read", Text: "safe body", From: name, To: []string{name}, AgentAcks: []string{name}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := h.m.View().Content
	for _, control := range []string{"\x1b[2J", "\rname", "\x07"} {
		if strings.Contains(view, control) {
			t.Fatalf("remote control reached display: %q", control)
		}
	}
	v3Contains(t, ansi.Strip(view), "acknowledged by")
	chatEqual(t, "render did not issue a command", h.m.State().InFlight, 0)
}

func TestContactRoutingAcceptanceExpiredUpgradeHoldDoesNotPromiseDelivery(t *testing.T) {
	h, _ := contactMessagesHarness(t, protocol.ContactMessageView{ID: "held", Cursor: 1, Direction: "out", State: "expired", Text: "expired payload", RefusalReason: protocol.ContactHeldForUpgrade})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "expired")
	if strings.Contains(view, "leaves once they upgrade") {
		t.Fatal("expired message promises delivery after upgrade")
	}
}
