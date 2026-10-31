package chat_test

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func addressChatHarness(t *testing.T, supported, dials bool) (*chatHarness, protocol.PairingContactView) {
	t.Helper()
	caps := slices.Clone(protocol.SessionCapabilities)
	caps = slices.DeleteFunc(caps, func(c string) bool { return c == "contact_address" })
	if supported {
		caps = append(caps, "contact_address")
	}
	h := pairingChatCapabilities(t, caps)
	h.input(tea.WindowSizeMsg{Width: 160, Height: 40})
	c := contactChatRow("sha256:"+strings.Repeat("a", 64), "Office", true)
	c.Destination, c.Dials = "old.example:7445", dials
	c.Summary = &protocol.PairingContactSummary{Sync: protocol.ContactSyncView{Dials: dials, LastFailure: "dial timeout", Failures: 1}}
	v2Command(h, "/pair status")
	req := pairingChatRequest(h, "status")
	h.reply(req, contactChatReply(c))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyEscape, 0))
	h.input(chatKey(tea.KeyTab, 0))
	req = h.take(protocol.TypeOperatorContact)
	page := contactMessagesPage(c.IdentityKey)
	page.ContactPage.Sync.Dials = dials
	h.reply(req, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h, c
}

func TestContactAddressAcceptanceTargetsSelectedIdentityAndWaitsForVerification(t *testing.T) {
	h, c := addressChatHarness(t, true, true)
	v2Command(h, "/pair address new.example:7445")
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "address operation", req.ContactOp, "address")
	chatEqual(t, "stable selected identity", req.ContactKey, c.IdentityKey)
	chatEqual(t, "candidate destination", req.ContactText, "new.example:7445")
	if req.MessageID == "" {
		t.Fatal("address mutation lacks operation ID")
	}
	v3Contains(t, ansi.Strip(h.m.View().Content), "old.example:7445")
	if strings.Contains(strings.ToLower(h.m.State().Status), "updated") {
		t.Fatal("claimed update before verified response")
	}
	c.Destination = "new.example:7445"
	response := contactChatReply(c)
	response.Type = protocol.TypeOperatorContactResult
	response.ContactOp = "address"
	response.ContactKey = c.IdentityKey
	h.reply(req, response)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, ansi.Strip(h.m.View().Content), "new.example:7445")
	chatEqual(t, "same contact after address change", contactChatViews(h)[0].Contact, c.IdentityKey)
}

func TestContactAddressAcceptanceRefusalKeepsOldDestination(t *testing.T) {
	h, _ := addressChatHarness(t, true, true)
	v2Command(h, "/pair address impostor.example:7445")
	req := h.take(protocol.TypeOperatorContact)
	h.reply(req, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrContactRefused, Detail: "identity mismatch"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "old.example:7445")
	v3Contains(t, strings.ToLower(h.m.State().Status), "identity mismatch")
}

func TestContactAddressAcceptanceUnsupportedAndWaitingSideDoNotSend(t *testing.T) {
	for _, tc := range []struct {
		name             string
		supported, dials bool
		want             string
	}{
		{"old broker", false, true, "support"},
		{"waiting side", true, false, "dial"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := addressChatHarness(t, tc.supported, tc.dials)
			v2Command(h, "/pair address new.example:7445")
			chatEqual(t, "no mutation request", h.m.State().InFlight, 0)
			chatEqual(t, "no queued mutation", h.m.State().Queued, 0)
			v3Contains(t, strings.ToLower(h.m.State().Status), tc.want)
		})
	}
}

func TestContactAddressAcceptanceInvalidDestinationDoesNotSend(t *testing.T) {
	for _, destination := range []string{"host", "host:0", "host:65536", "https://host:7445", "host:7445 extra"} {
		t.Run(destination, func(t *testing.T) {
			h, _ := addressChatHarness(t, true, true)
			v2Command(h, "/pair address "+destination)
			chatEqual(t, "invalid destination not sent", h.m.State().InFlight, 0)
			chatEqual(t, "invalid destination not queued", h.m.State().Queued, 0)
		})
	}
}

func TestContactAddressAcceptanceRequiresContactTab(t *testing.T) {
	h, _ := addressChatHarness(t, true, true)
	h.input(chatKey(tea.KeyTab, tea.ModShift))
	v2Command(h, "/pair address new.example:7445")
	chatEqual(t, "All does not address last contact", h.m.State().InFlight, 0)
	v3Contains(t, h.m.State().Status, "⇄")
}

func TestContactAddressAcceptanceBracketedIPv6PreservesCandidate(t *testing.T) {
	h, c := addressChatHarness(t, true, true)
	v2Command(h, "/pair address [2001:db8::14]:7445")
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "IPv6 candidate", req.ContactText, "[2001:db8::14]:7445")
	chatEqual(t, "IPv6 still targets paired identity", req.ContactKey, c.IdentityKey)
}

func TestContactAddressAcceptanceReplayShowsCurrentSnapshot(t *testing.T) {
	h, c := addressChatHarness(t, true, true)
	v2Command(h, "/pair address previous.example:7445")
	req := h.take(protocol.TypeOperatorContact)
	// A completed operation can be replayed after a newer update. Its
	// response acknowledges the old operation but reports the current state.
	c.Destination = "current.example:7445"
	response := contactChatReply(c)
	response.Type = protocol.TypeOperatorContactResult
	response.ContactOp = "address"
	response.ContactKey = c.IdentityKey
	h.reply(req, response)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, ansi.Strip(h.m.View().Content), "current.example:7445")
	if strings.Contains(h.m.State().Status, "previous.example:7445") {
		t.Fatal("replay claimed the obsolete candidate is current")
	}
}

func TestContactAddressAcceptancePartialSnapshotDoesNotInventDestination(t *testing.T) {
	h, c := addressChatHarness(t, true, true)
	v2Command(h, "/pair address new.example:7445")
	req := h.take(protocol.TypeOperatorContact)
	response := contactChatReply()
	response.Type = protocol.TypeOperatorContactResult
	response.ContactOp = "address"
	response.ContactKey = c.IdentityKey
	response.PairingResult.ContactsTotal = 129
	response.PairingResult.ContactsTruncated = true
	h.reply(req, response)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	status := strings.ToLower(h.m.State().Status)
	if strings.Contains(status, "messages go to") {
		t.Fatalf("omitted contact has an invented destination: %s", status)
	}
	v3Contains(t, status, "snapshot")
}
