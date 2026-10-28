package chat_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func contactChatRow(key, name string, confirmed bool) protocol.PairingContactView {
	return protocol.PairingContactView{IdentityKey: key, Name: name, EstablishedAt: time.Now(), Joined: true, Confirmed: confirmed}
}

func contactChatReply(rows ...protocol.PairingContactView) protocol.Message {
	return protocol.Message{Type: protocol.TypeOperatorPairingResult, PairingResult: &protocol.PairingSnapshot{
		Contacts: rows, ContactsTotal: len(rows), RedeemAvailable: true,
	}}
}

func contactChatBaseline(h *chatHarness, rows ...protocol.PairingContactView) {
	h.t.Helper()
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	quiet := pairingChatRequest(h, "list")
	h.reply(quiet, contactChatReply(rows...))
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func contactChatHarness(t *testing.T, rows ...protocol.PairingContactView) *chatHarness {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 120, Height: 30})
	contactChatBaseline(h, rows...)
	return h
}

func contactChatViews(h *chatHarness) []chat.ConversationView {
	var out []chat.ConversationView
	for _, view := range h.m.State().Conversations {
		if view.Contact != "" {
			out = append(out, view)
		}
	}
	return out
}

func TestContactsChatAcceptanceDeduplicatesIdentityAndRebuildsAfterReconnect(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	row := contactChatRow(key, "operator", false)
	h := contactChatHarness(t, row, row)
	views := contactChatViews(h)
	chatEqual(t, "one contact per identity", len(views), 1)
	chatEqual(t, "full fingerprint retained", views[0].Contact, key)
	chatEqual(t, "explicit contact distinct from group and All", views[0].Contact != "" && len(views[0].Group) == 0 && !views[0].All, true)
	h.input(chatKey(tea.KeyTab, 0))
	view := ansi.Strip(h.m.View().Content)
	for _, want := range []string{"⇄ workspace", `workspace "operator"`, key, "confirming", "messages between workspaces arrive in a later step"} {
		v3Contains(t, view, want)
	}
	h.reconnectTo(newChatScript(), "epoch")
	row.Confirmed = true
	contactChatBaseline(h, row)
	chatEqual(t, "reconnect does not duplicate contact", len(contactChatViews(h)), 1)
	v3Contains(t, ansi.Strip(h.m.View().Content), "confirmed")
	for _, size := range []tea.WindowSizeMsg{{Width: 45, Height: 16}, {Width: 120, Height: 30}} {
		h.input(size)
		v2FrameFits(t, h, size.Width, size.Height)
	}
}

func TestContactsChatAcceptanceSameNameLocalAgentRemainsSeparate(t *testing.T) {
	key := "sha256:" + strings.Repeat("e", 64)
	h := contactChatHarness(t, contactChatRow(key, "alice", true))
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
		Kind: protocol.EventPresenceChanged, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2},
		AgentID: "alice", SessionID: "local-alice", State: "connected",
	}}
	h.await(func() bool { return h.m.State().Revision == 2 })
	v2Command(h, "/chat alice")
	chatEqual(t, "agent still addressable by its name", h.m.State().Selected, "alice")
	for _, c := range contactChatViews(h) {
		chatEqual(t, "agent selection cannot select peer workspace", c.Selected, false)
	}
	v2Type(h, "local message")
	h.input(chatKey(tea.KeyEnter, 0))
	request := h.take(protocol.TypeOperatorSend)
	chatEqual(t, "local agent remains the recipient", request.To, "alice")
	chatEqual(t, "workspace contact not converted into agent", len(contactChatViews(h)), 1)
}

func TestContactsChatAcceptanceOldBrokerCannotSettle(t *testing.T) {
	caps := slices.DeleteFunc(slices.Clone(protocol.SessionCapabilities), func(cap string) bool { return cap == protocol.CapabilityPairingContacts })
	h := pairingChatCapabilities(t, caps)
	v2Command(h, "/pair settle one")
	chatEqual(t, "old broker not mutated", h.m.State().InFlight, 0)
	chatEqual(t, "no queued mutation", h.m.State().Queued, 0)
	chatEqual(t, "no transcript entry", len(h.m.State().Entries), 0)
}

func TestContactsChatAcceptanceComposerCannotSendToContact(t *testing.T) {
	key := "sha256:" + strings.Repeat("b", 64)
	h := contactChatHarness(t, contactChatRow(key, "alice", true))
	v2Type(h, "saved All draft")
	h.input(chatKey(tea.KeyTab, 0))
	v2Type(h, "must not leave this workspace")
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "no send intent", h.m.State().Intent, nil)
	chatEqual(t, "no network request", h.m.State().InFlight, 0)
	chatEqual(t, "no outgoing entry", len(h.m.State().Entries), 0)
	h.input(chatKey(tea.KeyTab, tea.ModShift))
	chatEqual(t, "All draft preserved", h.m.State().Draft, "saved All draft")
}

func TestContactsChatAcceptanceNamesAndFingerprintsCannotBecomeLocalRecipients(t *testing.T) {
	key := "sha256:" + strings.Repeat("c", 64)
	for _, name := range []string{"operator", "alice", key} {
		t.Run(name, func(t *testing.T) {
			h := contactChatHarness(t, contactChatRow(key, name, true))
			for _, command := range []string{"/to " + name, "/chat " + name} {
				v2Command(h, command)
				chatEqual(t, "contact cannot route via agent command", h.m.State().SelectedAll, true)
				chatEqual(t, "command does not send", h.m.State().Intent, nil)
				h.input(chatKey(tea.KeyEscape, 0))
			}
			v2Type(h, "@"+name)
			chatEqual(t, "contact is not an agent mention candidate", h.m.State().Picker == nil, true)
		})
	}
}

func TestContactsChatAcceptanceAcceptedUndeliveredRequestHasNoTab(t *testing.T) {
	h := contactChatHarness(t)
	v2Command(h, "/pair status")
	req := pairingChatRequest(h, "status")
	resp := contactChatReply()
	resp.PairingResult.Requests = []protocol.PairingRequestView{{IdentityKey: "sha256:" + strings.Repeat("d", 64), Name: "same name", State: "accepted"}}
	resp.PairingResult.RequestsTotal = 1
	h.reply(req, resp)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "no premature contact tab", len(contactChatViews(h)), 0)
	v3Contains(t, ansi.Strip(h.m.View().Content), "awaiting peer confirmation")
}

func TestContactsChatAcceptanceSettleUsesExactOperationAndStaysOutOfTranscript(t *testing.T) {
	for _, target := range []string{"", "full-operation-id"} {
		t.Run(target, func(t *testing.T) {
			h := contactChatHarness(t)
			v2Command(h, "/pair settle "+target)
			req := pairingChatRequest(h, "settle")
			chatEqual(t, "full target retained", req.PairingTarget, target)
			resp := contactChatReply()
			resp.PairingSettle = &protocol.PairingSettleView{Queued: 1, AlreadyQueued: 2, NotQueued: 3}
			h.reply(req, resp)
			h.await(func() bool { return h.m.State().InFlight == 0 })
			chatEqual(t, "settlement is not a message", len(h.m.State().Entries), 0)
		})
	}
}

func contactChatChanged(h *chatHarness, revision uint64) {
	h.t.Helper()
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
		Kind: "pairing_changed", Watermark: protocol.Watermark{Epoch: "epoch", Revision: revision},
	}}
	h.await(func() bool { return h.m.State().Revision == revision })
}

func TestContactsChatAcceptanceLiveEventsCoalesceAndKeepDraftAndIdentity(t *testing.T) {
	h := contactChatHarness(t)
	v2Type(h, "do not lose my draft")
	contactChatChanged(h, 2)
	first := pairingChatRequest(h, "list")
	for revision := uint64(3); revision <= 5; revision++ {
		contactChatChanged(h, revision)
	}
	chatEqual(t, "one quiet read in flight", h.m.State().InFlight, 1)
	chatEqual(t, "burst does not queue redundant reads", h.m.State().Queued, 0)
	key := "sha256:" + strings.Repeat("f", 64)
	row := contactChatRow(key, "colleague", false)
	h.reply(first, contactChatReply(row))
	second := pairingChatRequest(h, "list")
	row.Confirmed = true
	h.reply(second, contactChatReply(row))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "one contact after repeated events", len(contactChatViews(h)), 1)
	chatEqual(t, "quiet refresh preserves draft", h.m.State().Draft, "do not lose my draft")
	chatEqual(t, "quiet refresh creates no messages", len(h.m.State().Entries), 0)
	h.input(chatKey(tea.KeyTab, 0))
	v3Contains(t, ansi.Strip(h.m.View().Content), "confirmed on both sides")
	v3Contains(t, ansi.Strip(h.m.View().Content), key)
}

func TestContactsChatAcceptanceQuietRefreshNeverRevealsInvitationCode(t *testing.T) {
	h := contactChatHarness(t)
	contactChatChanged(h, 2)
	request := pairingChatRequest(h, "list")
	response := contactChatReply()
	response.PairingResult.Invitation = &protocol.InvitationView{ID: "remote-snapshot", Code: pairingTestCode}
	h.reply(request, response)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	pairingChatNoCode(h)
	chatEqual(t, "refresh cannot open a code panel", h.m.State().SelectedAll, true)
}
