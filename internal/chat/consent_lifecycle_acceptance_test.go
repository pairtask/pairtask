package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
	"strings"
	"testing"
)

func TestConsentLifecycleAcceptanceLateResponseCannotReopenDismissedOverlay(t *testing.T) {
	h := consentInputHarness(t)
	v2Command(h, "/pair consent "+consentFixtureID)
	req := h.take(protocol.TypeOperatorContact)
	if req.ContactOp != "consent_list" {
		t.Fatalf("opening consent wrote %q", req.ContactOp)
	}
	h.input(chatKey(tea.KeyEscape, 0))
	h.reply(req, consentInputPage())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	if strings.Contains(ansi.Strip(h.m.View().Content), consentFixtureText) {
		t.Fatal("late preview reopened dismissed overlay")
	}
	h.input(chatKey('y', tea.ModCtrl))
	consentInputNoWrites(t, h)
}

func TestConsentLifecycleAcceptanceMouseCannotSelectOrChangeDraft(t *testing.T) {
	h := consentInputHarness(t)
	h.input(tea.PasteMsg{Content: "draft with provenance"})
	before := h.m.State()
	consentInputOpen(t, h)
	for _, msg := range []tea.Msg{tea.MouseClickMsg{X: 20, Y: 10, Button: tea.MouseLeft}, tea.MouseWheelMsg{X: 20, Y: 10, Button: tea.MouseWheelDown}, tea.MouseClickMsg{X: 1, Y: 1, Button: tea.MouseLeft}} {
		h.input(msg)
	}
	h.input(chatKey('y', tea.ModCtrl))
	consentInputNoWrites(t, h)
	after := h.m.State()
	if before.Draft != after.Draft || before.Pasted != after.Pasted || before.Selected != after.Selected || before.ScrollOffset != after.ScrollOffset {
		t.Fatal("mouse escaped consent surface and changed editor or conversation")
	}
}

func TestConsentLifecycleAcceptanceViewingDoesNotCreateReceiptOrAutoOpen(t *testing.T) {
	h := consentInputHarness(t)
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: "consent_changed", Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	h.await(func() bool {
		for i := 0; i < len(h.requests); i++ {
			r := h.requests[i]
			if r.Type == protocol.TypeOperatorContact && r.ContactOp == "consent_list" {
				h.requests = append(h.requests[:i], h.requests[i+1:]...)
				h.reply(r, consentInputPage())
				i--
			}
		}
		return h.m.State().Revision >= 2 && h.m.State().InFlight == 0
	})
	if strings.Contains(ansi.Strip(h.m.View().Content), consentFixtureText) {
		t.Fatal("arrival automatically exposed pending content")
	}
	for i := 0; i < 3; i++ {
		h.m.View()
	}
	consentInputNoWrites(t, h)
	if h.m.State().Unread != 0 || len(h.m.State().Entries) != 0 {
		t.Fatal("held request entered ordinary conversation or unread state")
	}
}

func TestConsentLifecycleAcceptanceRefreshInvalidatesSelectedChoice(t *testing.T) {
	h := consentInputHarness(t)
	consentInputOpen(t, h)
	h.input(chatKey('3', 0))
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: "consent_changed", Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	h.await(func() bool {
		for i := 0; i < len(h.requests); i++ {
			r := h.requests[i]
			if r.Type == protocol.TypeOperatorContact && r.ContactOp == "consent_list" {
				h.requests = append(h.requests[:i], h.requests[i+1:]...)
				h.reply(r, consentInputPage())
				i--
			}
		}
		return h.m.State().Revision >= 2 && h.m.State().InFlight == 0
	})
	h.input(chatKey('y', tea.ModCtrl))
	consentInputNoWrites(t, h)
}

func TestConsentLifecycleAcceptanceReplacementOpenCannotUseOlderResponse(t *testing.T) {
	h := consentInputHarness(t)
	v2Command(h, "/pair consent "+consentFixtureID)
	old := h.take(protocol.TypeOperatorContact)
	h.input(chatKey(tea.KeyEscape, 0))
	v2Command(h, "/pair consent replacement-request-293")
	current := h.take(protocol.TypeOperatorContact)
	response := consentInputPage()
	response.ConsentPage.Items[0].ID = "replacement-request-293"
	response.ConsentPage.Items[0].Text = "REPLACEMENT PRIVATE BODY"
	h.reply(current, response)
	h.await(func() bool { return strings.Contains(ansi.Strip(h.m.View().Content), "REPLACEMENT PRIVATE BODY") })
	h.input(chatKey('1', 0))
	h.reply(old, consentInputPage())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	if strings.Contains(ansi.Strip(h.m.View().Content), consentFixtureText) {
		t.Fatal("superseded response replaced current immutable request")
	}
	h.input(chatKey('y', tea.ModCtrl))
	req := h.take(protocol.TypeOperatorContact)
	if req.ConsentDecide == nil || req.ConsentDecide.ConsentID != "replacement-request-293" {
		t.Fatalf("old response retargeted selected decision: %+v", req)
	}
}
