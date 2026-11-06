package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
	"testing"
)

func TestConsentRefreshAcceptanceCannotConfirmOldSnapshotWhileLoading(t *testing.T) {
	h := consentInputHarness(t)
	consentInputOpen(t, h)
	h.input(chatKey('3', 0))
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: "consent_changed", Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	refresh := h.take(protocol.TypeOperatorContact)
	if refresh.ContactOp != "consent_list" {
		t.Fatalf("refresh wrote mutation: %q", refresh.ContactOp)
	}
	h.input(chatKey('1', 0))
	h.input(chatKey('y', tea.ModCtrl))
	if h.m.State().InFlight != 1 {
		t.Fatalf("old preview could decide while refresh was withheld: in-flight=%d", h.m.State().InFlight)
	}
	h.reply(refresh, consentInputPage())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey('y', tea.ModCtrl))
	consentInputNoWrites(t, h)
}

func TestConsentRefreshAcceptanceFullIDCanBeOpenedBeyondFirstPage(t *testing.T) {
	h := consentInputHarness(t)
	v2Command(h, "/pair consent "+consentFixtureID)
	first := h.take(protocol.TypeOperatorContact)
	response := consentInputPage()
	response.ConsentPage.Items[0].ID = "other-pending-request"
	response.ConsentPage.More = true
	response.ConsentPage.Next = 41
	h.reply(first, response)
	var next *protocol.Message
	h.await(func() bool {
		for i, r := range h.requests {
			if r.Type == protocol.TypeOperatorContact && r.ContactOp == "consent_list" {
				copy := r
				next = &copy
				h.requests = append(h.requests[:i], h.requests[i+1:]...)
				return true
			}
		}
		return h.m.State().InFlight == 0
	})
	if next == nil {
		t.Fatal("full immutable consent ID could not be opened beyond first page")
	}
	if next.ContactConsentBefore != 41 {
		t.Fatalf("consent pagination lost frozen cursor: %d", next.ContactConsentBefore)
	}
	h.reply(*next, consentInputPage())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey('1', 0))
	h.input(chatKey('y', tea.ModCtrl))
	decision := h.take(protocol.TypeOperatorContact)
	if decision.ConsentDecide == nil || decision.ConsentDecide.ConsentID != consentFixtureID {
		t.Fatalf("pagination selected wrong request: %+v", decision)
	}
}
