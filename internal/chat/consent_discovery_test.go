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
	discoveryOpenList(t, h)
	for _, msg := range []tea.Msg{chatKey('1', 0), chatKey('y', tea.ModCtrl), tea.PasteMsg{Content: "x"}} {
		h.input(msg)
	}
	discoveryNoWrites(t, h)
	discoveryPreviewAndBack(t, h)
	h.input(chatKey(tea.KeyEnter, 0))
	h.input(chatKey('4', 0))
	h.input(chatKey('y', tea.ModCtrl))
	requireDecision(t, h.take(protocol.TypeOperatorContact), protocol.ConsentDecline)
}

// discoveryOpenList opens the held contact's list, which names its request
// without previewing it.
func discoveryOpenList(t *testing.T, h *chatHarness) {
	t.Helper()
	v2Command(h, "/pair consent contact:"+discoveryToken)
	read := h.take(protocol.TypeOperatorContact)
	requireContactOp(t, read, protocol.ContactOpConsentList)
	h.reply(read, consentInputPage())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := discoveryView(h)
	if !strings.Contains(view, consentFixtureID) {
		t.Fatalf("list should name the request: %s", view)
	}
	if strings.Contains(view, consentFixtureText) {
		t.Fatalf("list should not preview the request: %s", view)
	}
}

// discoveryPreviewAndBack previews the highlighted request, confirms
// nothing unselected, and returns to the list with Esc.
func discoveryPreviewAndBack(t *testing.T, h *chatHarness) {
	t.Helper()
	h.input(chatKey(tea.KeyEnter, 0))
	if !strings.Contains(discoveryView(h), consentFixtureText) {
		t.Fatal("Enter did not preview the highlighted request")
	}
	h.input(chatKey('y', tea.ModCtrl)) // nothing selected: nothing sent
	discoveryNoWrites(t, h)
	h.input(chatKey(tea.KeyEscape, 0))
	v := discoveryView(h)
	if strings.Contains(v, consentFixtureText) {
		t.Fatal("Esc in the preview still shows the request")
	}
	if !strings.Contains(v, "Held requests from") {
		t.Fatal("Esc in the preview did not return to the list")
	}
}

// discoveryContact is the fixture contact's key.
const discoveryContact = "authenticated-key-293"

// requireContactOp fails unless req is op for the fixture's contact.
func requireContactOp(t *testing.T, req protocol.Message, op string) {
	t.Helper()
	if req.ContactOp != op {
		t.Fatalf("sent %q, want %q: %+v", req.ContactOp, op, req)
	}
	if req.ContactKey != discoveryContact {
		t.Fatalf("%s for %q, want %q", op, req.ContactKey, discoveryContact)
	}
}

// requireDecision fails unless req decides the fixture's request as choice.
func requireDecision(t *testing.T, req protocol.Message, choice string) {
	t.Helper()
	d := req.ConsentDecide
	if d == nil {
		t.Fatalf("no decision in %+v", req)
	}
	if d.ConsentID != consentFixtureID || d.Choice != choice {
		t.Fatalf("decision %+v", d)
	}
}

// requireConsentOn fails unless req turns consent on (or off) as want.
func requireConsentOn(t *testing.T, req protocol.Message, want bool) {
	t.Helper()
	if req.ConsentOn == nil || *req.ConsentOn != want {
		t.Fatalf("consent on %v, want %v: %+v", req.ConsentOn, want, req)
	}
}

// requireResetScope fails unless req resets exactly agent's operation.
func requireResetScope(t *testing.T, req protocol.Message, agent, operation string) {
	t.Helper()
	if req.ConsentAgent != agent || req.ConsentOperation != operation {
		t.Fatalf("reset sent %+v", req)
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
		requireContactOp(t, req, protocol.ContactOpConsentOpt)
		requireConsentOn(t, req, word == "on")
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
	requireContactOp(t, req, protocol.ContactOpConsentReset)
	requireResetScope(t, req, "alpha", "review")
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
