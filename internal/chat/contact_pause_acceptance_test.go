package chat_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func pauseChatContact(t *testing.T, paused bool) protocol.PairingContactView {
	t.Helper()
	c := contactChatRow("sha256:"+strings.Repeat("a", 64), "Peer", true)
	if paused {
		if err := json.Unmarshal([]byte(`{"paused_at":"2026-11-01T12:00:00Z"}`), &c); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func pauseChatHarness(t *testing.T, paused, supported bool) (*chatHarness, protocol.PairingContactView) {
	t.Helper()
	caps := slices.DeleteFunc(slices.Clone(protocol.SessionCapabilities), func(c string) bool { return c == "contact_pause" })
	if supported {
		caps = append(caps, "contact_pause")
	}
	h := pairingChatCapabilities(t, caps)
	c := pauseChatContact(t, paused)
	v2Command(h, "/pair status")
	req := pairingChatRequest(h, "status")
	h.reply(req, contactChatReply(c))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h, c
}

func pauseChatOpen(t *testing.T, h *chatHarness, c protocol.PairingContactView) {
	t.Helper()
	h.input(chatKey(tea.KeyEscape, 0))
	h.input(chatKey(tea.KeyTab, 0))
	req := h.take(protocol.TypeOperatorContact)
	h.reply(req, contactMessagesPage(c.IdentityKey))
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func TestContactPauseChatAcceptanceStatusCardAndComposerStayHonest(t *testing.T) {
	h, c := pauseChatHarness(t, true, true)
	v3Contains(t, ansi.Strip(h.m.View().Content), "paused by you")
	v3Contains(t, ansi.Strip(h.m.View().Content), "/pair resume")
	pauseChatOpen(t, h, c)
	v3Contains(t, ansi.Strip(h.m.View().Content), "paused by you")
	v2Type(h, "kept paused draft")
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "paused send never reaches broker", h.m.State().InFlight, 0)
	chatEqual(t, "paused draft preserved", h.m.State().Draft, "kept paused draft")
	v3Contains(t, h.m.State().Status, "paused: /pair resume to send")
}

func TestContactPauseChatAcceptanceCommandsUseSelectedIdentityAndWaitForResponse(t *testing.T) {
	for _, op := range []string{"pause", "resume"} {
		t.Run(op, func(t *testing.T) {
			h, c := pauseChatHarness(t, op == "resume", true)
			pauseChatOpen(t, h, c)
			v2Command(h, "/pair "+op)
			req := h.take(protocol.TypeOperatorContact)
			chatEqual(t, "operation", req.ContactOp, op)
			chatEqual(t, "identity", req.ContactKey, c.IdentityKey)
			chatEqual(t, "operation ID", req.MessageID != "", true)
			chatEqual(t, "pending operation does not change card", strings.Contains(ansi.Strip(h.m.View().Content), "paused by you"), op == "resume")
			updated := pauseChatContact(t, op == "pause")
			out := contactChatReply(updated)
			out.Type = protocol.TypeOperatorContactResult
			out.ContactOp = op
			out.ContactKey = c.IdentityKey
			h.reply(req, out)
			h.await(func() bool { return h.m.State().InFlight == 0 })
			chatEqual(t, "same selected contact", contactChatViews(h)[0].Contact, c.IdentityKey)
			chatEqual(t, "card changes only after response", strings.Contains(ansi.Strip(h.m.View().Content), "paused by you"), op == "pause")
		})
	}
}

func TestContactPauseChatAcceptanceUnsupportedOrWrongTabNeverMutates(t *testing.T) {
	for _, supported := range []bool{false, true} {
		h, c := pauseChatHarness(t, false, supported)
		if !supported {
			pauseChatOpen(t, h, c)
		}
		for _, op := range []string{"pause", "resume"} {
			v2Command(h, "/pair "+op)
			chatEqual(t, "no unsupported or wrong-tab mutation", h.m.State().InFlight, 0)
			chatEqual(t, "nothing queued", h.m.State().Queued, 0)
		}
	}
}

func TestContactPauseChatAcceptanceVisibleMessagesCanStillBeRead(t *testing.T) {
	h, c := pauseChatHarness(t, true, true)
	pauseChatOpen(t, h, c)
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventContactMessagesChanged, ContactKey: c.IdentityKey, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	refresh := h.take(protocol.TypeOperatorContact)
	h.reply(refresh, contactMessagesPage(c.IdentityKey, contactUnreadRows(1)...))
	read := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "reading allowed", read.ContactOp, protocol.ContactOpRead)
	if !slices.Equal(read.ContactRead, []string{"incoming-1"}) {
		t.Fatalf("wrong read IDs: %v", read.ContactRead)
	}
}
