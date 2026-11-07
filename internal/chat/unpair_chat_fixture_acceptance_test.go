package chat_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
)

type unpairChatOptions struct {
	supported, ended, open bool
	epoch                  int64
}

func unpairChatContact(t *testing.T, opts unpairChatOptions) protocol.PairingContactView {
	t.Helper()
	c := contactChatRow("sha256:"+strings.Repeat("a", 64), "Office", true)
	epoch := opts.epoch
	if epoch == 0 {
		epoch = 7
	}
	fields := map[string]any{"pairing_epoch": epoch, "legacy": false}
	if opts.ended {
		fields["unpaired_at"] = "2026-11-07T06:00:00Z"
		c.Acks = &protocol.ContactAckSummary{Unacked: 1, AckUpto: 1}
	}
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}
func unpairChatSnapshot(t *testing.T, c protocol.PairingContactView) protocol.Message {
	t.Helper()
	wire, err := json.Marshal(map[string]any{"type": "operator_pairing_result", "pairing_result": map[string]any{"contacts": []any{c}, "contacts_total": 1, "unpair_available": true, "repair_available": true, "redeem_available": true}})
	if err != nil {
		t.Fatal(err)
	}
	var m protocol.Message
	if err = json.Unmarshal(wire, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
func unpairChatHarness(t *testing.T, opts unpairChatOptions) (*chatHarness, protocol.PairingContactView) {
	t.Helper()
	caps := slices.DeleteFunc(slices.Clone(protocol.SessionCapabilitiesFor(25)), func(c string) bool { return c == "contact_unpair" || c == "pairing_repair" })
	if opts.supported {
		caps = append(caps, "contact_unpair", "pairing_repair")
	}
	h := pairingChatCapabilities(t, caps)
	h.input(tea.WindowSizeMsg{Width: 180, Height: 60})
	c := unpairChatContact(t, opts)
	v2Command(h, "/pair status")
	h.reply(pairingChatRequest(h, "status"), unpairChatSnapshot(t, c))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyEscape, 0))
	if opts.open {
		unpairChatOpen(t, h, c)
	}
	return h, c
}
func unpairChatOpen(t *testing.T, h *chatHarness, c protocol.PairingContactView) {
	t.Helper()
	h.input(chatKey(tea.KeyTab, 0))
	req := h.take(protocol.TypeOperatorContact)
	page := contactMessagesPage(c.IdentityKey)
	page.ContactPage.Acks = c.Acks
	h.reply(req, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
}
func unpairChatNoRequest(t *testing.T, h *chatHarness) {
	t.Helper()
	chatEqual(t, "no unpair request in flight", h.m.State().InFlight, 0)
	chatEqual(t, "no queued mutation", h.m.State().Queued, 0)
}
func unpairChatFields(t *testing.T, req protocol.Message) map[string]any {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

const unpairChatPhrase = "/pair unpair confirm aaaaaaaa"
