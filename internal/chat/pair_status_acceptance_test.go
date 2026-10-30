package chat_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPairStatusAcceptanceNamesTimesAndDialOwnershipAreExplicit(t *testing.T) {
	h := pairingChatHarness(t, true)
	h.input(tea.WindowSizeMsg{Width: 160, Height: 60})
	key := "sha256:" + strings.Repeat("a", 64)
	requested := time.Date(2026, 10, 30, 12, 34, 0, 0, time.Local)
	confirmed := requested.Add(time.Hour)
	v2Command(h, "/pair status")
	req := pairingChatRequest(h, "status")
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorPairingResult, PairingResult: &protocol.PairingSnapshot{
		SelfIdentity: "sha256:" + strings.Repeat("b", 64), SelfName: "My laptop",
		Requests: []protocol.PairingRequestView{{IdentityKey: key, Name: "Remote host", State: "accepted", FirstPairedAt: requested, DeliveredAt: &confirmed}}, RequestsTotal: 1,
		Joins: []protocol.PairingJoinView{{OperationID: "join-one", Destination: "192.168.1.32:7445", Name: "OUR advertised name", Peer: key, State: "contact", CreatedAt: requested, UpdatedAt: confirmed}}, JoinsTotal: 1,
		Contacts: []protocol.PairingContactView{{IdentityKey: key, Name: "Remote host", LocalName: "Office", Destination: "192.168.1.32:7445", Dials: false, Joined: true, Invited: true, Confirmed: true, EstablishedAt: confirmed}}, ContactsTotal: 1,
	}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := ansi.Strip(h.m.View().Content)
	for _, want := range []string{"My laptop", "Office", "requested Oct 30 12:34", "confirmed Oct 30 13:34", "started Oct 30 12:34", "updated Oct 30 13:34", "Paired workspaces", "waits to be dialed"} {
		v3Contains(t, view, want)
	}
	if strings.Contains(view, "with OUR advertised name") {
		t.Fatal("our redemption name was presented as the remote workspace")
	}
	if strings.Contains(view, "this side dials") {
		t.Fatal("destination presence overrode actual reciprocal dial ownership")
	}
}

func nameChatHarness(t *testing.T, supported bool) (*chatHarness, protocol.PairingContactView) {
	t.Helper()
	caps := slices.Clone(protocol.SessionCapabilities)
	if !supported {
		caps = slices.DeleteFunc(caps, func(c string) bool { return c == "contact_names" })
	}
	h := pairingChatCapabilities(t, caps)
	c := contactChatRow("sha256:"+strings.Repeat("c", 64), "Peer name", true)
	v2Command(h, "/pair status")
	req := pairingChatRequest(h, "status")
	h.reply(req, contactChatReply(c))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyEscape, 0))
	h.input(chatKey(tea.KeyTab, 0))
	page := h.take(protocol.TypeOperatorContact)
	h.reply(page, contactMessagesPage(c.IdentityKey))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h, c
}

func TestPairStatusAcceptanceLocalNameUpdatesTabAndKeepsSendIdentity(t *testing.T) {
	h, c := nameChatHarness(t, true)
	v2Command(h, "/pair name Home Mac")
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "rename operation", req.ContactOp, "name")
	chatEqual(t, "rename identity", req.ContactKey, c.IdentityKey)
	chatEqual(t, "multiword name", req.ContactText, "Home Mac")
	c.LocalName = "Home Mac"
	resp := contactChatReply(c)
	resp.Type = protocol.TypeOperatorContactResult
	h.reply(req, resp)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	views := contactChatViews(h)
	chatEqual(t, "tab label updates", views[0].Label, "⇄ Home Mac")
	chatEqual(t, "tab identity remains", views[0].Contact, c.IdentityKey)
	v2Type(h, "hello")
	h.input(chatKey(tea.KeyEnter, 0))
	sent := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "messages still address identity", sent.ContactKey, c.IdentityKey)
}

func TestPairStatusAcceptanceOldBrokerRefusesNameBeforeWriting(t *testing.T) {
	h, _ := nameChatHarness(t, false)
	v2Command(h, "/pair name Home Mac")
	chatEqual(t, "old broker receives no rename", h.m.State().InFlight, 0)
	chatEqual(t, "old broker queues no rename", h.m.State().Queued, 0)
	v3Contains(t, strings.ToLower(h.m.State().Status), "support")
}
