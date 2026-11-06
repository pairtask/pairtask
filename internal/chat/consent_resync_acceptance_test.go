package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
	"testing"
)

func TestConsentResyncAcceptanceSnapshotInvalidatesSelectedAuthority(t *testing.T) {
	cases := []protocol.SessionEvent{
		{Kind: protocol.EventResyncRequired, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}},
		{Kind: protocol.EventConsentChanged, Watermark: protocol.Watermark{Epoch: "new-epoch", Revision: 1}},
		{Kind: protocol.EventConsentChanged, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 5}},
	}
	for _, event := range cases {
		t.Run(event.Watermark.Epoch+"/"+event.Kind, func(t *testing.T) { consentResyncRejectsOldChoice(t, event) })
	}
}
func consentResyncRejectsOldChoice(t *testing.T, event protocol.SessionEvent) {
	t.Helper()
	h := consentInputHarness(t)
	consentInputOpen(t, h)
	h.input(chatKey('3', 0))
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &event}
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: event.Watermark}})
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey('y', tea.ModCtrl))
	// Choosing again on the stale preview still cannot authorize a write.
	h.input(chatKey('3', 0))
	h.input(chatKey('y', tea.ModCtrl))
	if h.m.State().InFlight != 0 || h.m.State().Queued != 0 {
		t.Fatal("resync/snapshot retained a prior Always choice and dispatched or queued it without reselection")
	}
}
