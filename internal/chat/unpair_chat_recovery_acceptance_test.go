package chat_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func unpairChatBaseline(h *chatHarness, c protocol.PairingContactView) {
	h.t.Helper()
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	req := pairingChatRequest(h, "list")
	h.reply(req, unpairChatSnapshot(h.t, c))
	h.await(func() bool { return h.m.State().InFlight == 0 })
}
func TestUnpairChatRecoveryAcceptanceReconnectDiscardsUnsentCard(t *testing.T) {
	h, c := unpairChatHarness(t, unpairChatOptions{supported: true})
	v2Command(h, "/pair unpair Office")
	h.m.View()
	h.reconnectTo(newChatScript(), "epoch")
	unpairChatBaseline(h, c)
	v2Command(h, unpairChatPhrase)
	unpairChatNoRequest(t, h)
}
func TestUnpairChatRecoveryAcceptanceSnapshotChangeDiscardsUnsentCard(t *testing.T) {
	for _, change := range []string{"epoch", "label", "pause"} {
		t.Run(change, func(t *testing.T) {
			h, _ := unpairChatHarness(t, unpairChatOptions{supported: true})
			v2Command(h, "/pair unpair Office")
			h.m.View()
			h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventPairingChanged, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
			req := h.take(protocol.TypeOperatorPairing)
			updated := unpairChatContact(t, unpairChatOptions{epoch: 7})
			switch change {
			case "epoch":
				updated = unpairChatContact(t, unpairChatOptions{epoch: 8})
			case "label":
				updated.LocalName = "Renamed workspace"
			case "pause":
				at := time.Date(2026, 11, 7, 7, 0, 0, 0, time.UTC)
				updated.PausedAt = &at
			}
			h.reply(req, unpairChatSnapshot(t, updated))
			h.await(func() bool { return h.m.State().InFlight == 0 })
			v2Command(h, unpairChatPhrase)
			unpairChatNoRequest(t, h)
		})
	}
}
func TestUnpairChatRecoveryAcceptanceConfirmedUnknownOutcomeRetriesFrozenRequest(t *testing.T) {
	h, _ := unpairChatHarness(t, unpairChatOptions{supported: true})
	v2Command(h, "/pair unpair Office")
	h.m.View()
	v2Command(h, unpairChatPhrase)
	first := h.take(protocol.TypeOperatorContact)
	h.reconnectTo(newChatScript(), "epoch")
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}})
	retry := h.take(protocol.TypeOperatorContact)
	first.RequestID = ""
	retry.RequestID = ""
	if !reflect.DeepEqual(first, retry) {
		t.Fatalf("unpair retry changed frozen identity/epoch/operation: first=%+v retry=%+v", first, retry)
	}
}
