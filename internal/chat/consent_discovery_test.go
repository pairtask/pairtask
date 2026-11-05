package chat_test

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

// The held contact's token: the fixture's contact is not in the pairing
// snapshot, so it is named by a short key's worth of its key.
const discoveryToken = "authenticate"

func discoveryView(h *chatHarness) string { return ansi.Strip(h.m.View().Content) }

// discoveryArrival announces a held request and answers the counts read.
func discoveryArrival(t *testing.T, h *chatHarness) {
	t.Helper()
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventConsentChanged,
		Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	read := h.take(protocol.TypeOperatorContact)
	if read.ContactOp != protocol.ContactOpConsentList || read.ContactKey != "" {
		t.Fatalf("arrival wrote %q for %q, want a consent_list of every contact", read.ContactOp, read.ContactKey)
	}
	h.reply(read, consentInputPage())
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

// discoveryNoWrites drains the wire and fails on any request.
func discoveryNoWrites(t *testing.T, h *chatHarness) {
	t.Helper()
	consentInputNoWrites(t, h)
}

func TestConsentDiscoveryArrivalCountsWithoutOpening(t *testing.T) {
	h := consentInputHarness(t)
	v2Type(h, "my draft")
	discoveryArrival(t, h)
	view := discoveryView(h)
	if strings.Contains(view, consentFixtureText) || h.m.State().Panel != "" {
		t.Fatalf("arrival opened something: panel=%q", h.m.State().Panel)
	}
	if !strings.Contains(view, "1 held for you") {
		t.Fatalf("footer does not count the held request: %s", view)
	}
	if h.m.State().Draft != "my draft" {
		t.Fatal("arrival changed the draft")
	}
	discoveryNoWrites(t, h)
}

func TestConsentDiscoveryBareCommandListsContacts(t *testing.T) {
	h := consentInputHarness(t)
	v2Command(h, "/pair consent")
	read := h.take(protocol.TypeOperatorContact)
	h.reply(read, consentInputPage())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := discoveryView(h)
	if !strings.Contains(view, discoveryToken+": 1 waiting — /pair consent contact:"+discoveryToken) {
		t.Fatalf("no per-contact line: %s", view)
	}
	if strings.Contains(view, consentFixtureText) {
		t.Fatal("bare /pair consent opened a request")
	}
}

func TestConsentDiscoveryBareCommandWithNoneSaysSo(t *testing.T) {
	h := consentInputHarness(t)
	v2Command(h, "/pair consent")
	read := h.take(protocol.TypeOperatorContact)
	empty := consentInputPage()
	empty.ConsentPage.Items, empty.ConsentPage.Total = nil, 0
	h.reply(read, empty)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	if got := h.m.State().Status; got != "no requests are held for your decision" {
		t.Fatalf("status %q", got)
	}
}

func TestConsentDiscoveryContactOpensListThenPreview(t *testing.T) {
	h := consentInputHarness(t)
	discoveryArrival(t, h)
	v2Command(h, "/pair consent contact:"+discoveryToken)
	read := h.take(protocol.TypeOperatorContact)
	if read.ContactOp != protocol.ContactOpConsentList || read.ContactKey != "authenticated-key-293" {
		t.Fatalf("contact list read %q for %q", read.ContactOp, read.ContactKey)
	}
	h.reply(read, consentInputPage())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := discoveryView(h)
	if !strings.Contains(view, consentFixtureID) || strings.Contains(view, consentFixtureText) {
		t.Fatalf("list should name the request without previewing it: %s", view)
	}
	for _, msg := range []tea.Msg{chatKey('1', 0), chatKey('y', tea.ModCtrl), tea.PasteMsg{Content: "x"}} {
		h.input(msg)
	}
	discoveryNoWrites(t, h)
	h.input(chatKey(tea.KeyEnter, 0))
	if !strings.Contains(discoveryView(h), consentFixtureText) {
		t.Fatal("Enter did not preview the highlighted request")
	}
	h.input(chatKey('y', tea.ModCtrl)) // nothing selected: nothing sent
	discoveryNoWrites(t, h)
	h.input(chatKey(tea.KeyEscape, 0))
	if v := discoveryView(h); strings.Contains(v, consentFixtureText) || !strings.Contains(v, "Held requests from") {
		t.Fatal("Esc in the preview did not return to the list")
	}
	h.input(chatKey(tea.KeyEnter, 0))
	h.input(chatKey('4', 0))
	h.input(chatKey('y', tea.ModCtrl))
	decide := h.take(protocol.TypeOperatorContact)
	if decide.ConsentDecide == nil || decide.ConsentDecide.ConsentID != consentFixtureID || decide.ConsentDecide.Choice != protocol.ConsentDecline {
		t.Fatalf("decision %+v", decide.ConsentDecide)
	}
}

func TestConsentDiscoveryUnknownContactRefused(t *testing.T) {
	h := consentInputHarness(t)
	v2Command(h, "/pair consent contact:nobody")
	if !strings.Contains(h.m.State().Status, "no paired workspace nobody") {
		t.Fatalf("status %q", h.m.State().Status)
	}
	discoveryNoWrites(t, h)
}

func TestConsentDiscoveryOptInOutSendWireOp(t *testing.T) {
	for _, word := range []string{"on", "off"} {
		h := consentInputHarness(t)
		discoveryArrival(t, h)
		v2Command(h, "/pair consent "+word+" "+discoveryToken)
		req := h.take(protocol.TypeOperatorContact)
		if req.ContactOp != protocol.ContactOpConsentOpt || req.ContactKey != "authenticated-key-293" || req.ConsentOn == nil || *req.ConsentOn != (word == "on") {
			t.Fatalf("%s sent %+v", word, req)
		}
		h.reply(req, protocol.Message{Type: protocol.TypeOperatorContactResult, ContactOp: protocol.ContactOpConsentOpt})
		h.await(func() bool { return h.m.State().InFlight == 0 })
		if !strings.HasPrefix(h.m.State().Status, "consent "+word+" for ") {
			t.Fatalf("result line %q", h.m.State().Status)
		}
	}
}

func TestConsentDiscoveryOptRefusalLine(t *testing.T) {
	h := consentInputHarness(t)
	discoveryArrival(t, h)
	v2Command(h, "/pair consent on "+discoveryToken)
	req := h.take(protocol.TypeOperatorContact)
	h.reply(req, protocol.Message{Type: protocol.TypeError, Code: "paused", Detail: "contact paused"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	if !strings.Contains(h.m.State().Status, "refused: contact paused") {
		t.Fatalf("refusal line %q", h.m.State().Status)
	}
}

func TestConsentDiscoveryResetSendsExactScope(t *testing.T) {
	h := consentInputHarness(t)
	discoveryArrival(t, h)
	v2Command(h, "/pair consent reset "+discoveryToken+" alpha review")
	req := h.take(protocol.TypeOperatorContact)
	if req.ContactOp != protocol.ContactOpConsentReset || req.ConsentAgent != "alpha" || req.ConsentOperation != "review" || req.ContactKey != "authenticated-key-293" {
		t.Fatalf("reset sent %+v", req)
	}
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorContactResult, ContactOp: protocol.ContactOpConsentReset})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	if !strings.Contains(h.m.State().Status, "reset review to alpha from "+discoveryToken+" to undecided") {
		t.Fatalf("result line %q", h.m.State().Status)
	}
}

func TestConsentDiscoveryPartialResetOnlyShowsScopes(t *testing.T) {
	// No agents are known here, so the contact alone offers the command's
	// form; a named agent offers each of its operations.
	for line, want := range map[string]string{"/pair consent reset " + discoveryToken: "/pair consent reset " + discoveryToken + " AGENT OPERATION",
		"/pair consent reset " + discoveryToken + " alpha": "/pair consent reset " + discoveryToken + " alpha publish"} {
		h := consentInputHarness(t)
		discoveryArrival(t, h)
		v2Command(h, line)
		discoveryNoWrites(t, h)
		view := discoveryView(h)
		if !strings.Contains(view, want) {
			t.Fatalf("%s: scope help lacks the exact command: %s", line, view)
		}
	}
}

func TestConsentDiscoveryResetRejectsUnknownOperation(t *testing.T) {
	h := consentInputHarness(t)
	discoveryArrival(t, h)
	v2Command(h, "/pair consent reset "+discoveryToken+" alpha everything")
	discoveryNoWrites(t, h)
	if !strings.Contains(h.m.State().Status, "message, review or publish") {
		t.Fatalf("status %q", h.m.State().Status)
	}
}

func TestConsentDiscoveryOldBrokerSendsNothing(t *testing.T) {
	s := newChatScript()
	h := &chatHarness{t: t, wire: s, results: make(chan tea.Msg, 128), done: make(chan struct{}), dials: make(chan chatDialResult, 8)}
	var caps []string
	for _, c := range protocol.SessionCapabilities {
		if c != protocol.CapabilityConsentPrompts {
			caps = append(caps, c)
		}
	}
	h.dials <- chatDialResult{wire: s, session: protocol.SessionInfo{SessionID: "old", Epoch: "epoch", Capabilities: caps}}
	h.m = chat.New(chat.Options{Dial: func(context.Context) (chat.Transport, protocol.SessionInfo, error) {
		select {
		case d := <-h.dials:
			return d.wire, d.session, d.err
		case <-h.done:
			return nil, protocol.SessionInfo{}, context.Canceled
		}
	}})
	t.Cleanup(func() { close(h.done); s.Close() })
	h.launch(h.m.Init())
	h.input(tea.WindowSizeMsg{Width: 160, Height: 50})
	req := h.take(protocol.TypeOperatorSnapshot)
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 1}}})
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	for _, line := range []string{"/pair consent", "/pair consent on x", "/pair consent reset x a message", "/pair consent some-id"} {
		v2Command(h, line)
		if got := h.m.State().Status; got != "consent prompts need a newer broker" {
			t.Fatalf("%s: status %q", line, got)
		}
		consentInputNoWrites(t, h)
	}
}
