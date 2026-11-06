package chat_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

type pairedTabRoute struct {
	peer, here []string
	operator   bool
}

func pairedTabStoredRow(t *testing.T, key string, route pairedTabRoute) protocol.ContactMessageView {
	t.Helper()
	data, err := json.Marshal(map[string]any{"id": "stored-routing-327", "cursor": 7, "contact": key, "direction": "out", "state": "queued", "text": "STORED BODY @forged-agent@here", "created_at": time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC), "to": route.peer, "here": route.here, "operator_here": route.operator})
	if err != nil {
		t.Fatal(err)
	}
	var row protocol.ContactMessageView
	if err = json.Unmarshal(data, &row); err != nil {
		t.Fatal(err)
	}
	return row
}
func pairedTabMessageHead(t *testing.T, h *chatHarness) string {
	t.Helper()
	for _, line := range strings.Split(pairedTabView(h), "\n") {
		if strings.Contains(line, "you →") {
			return line
		}
	}
	t.Fatal("stored outbound message has no rendered recipient header")
	return ""
}
func pairedTabAssertStoredHead(t *testing.T, h *chatHarness, route pairedTabRoute) {
	t.Helper()
	head := pairedTabMessageHead(t, h)
	for _, agent := range route.peer {
		v3Contains(t, head, agent+"@Office")
	}
	for _, agent := range route.here {
		v3Contains(t, head, agent+"@here")
	}
	pairedTabAssertOperator(t, head, route.operator)
	if len(route.peer) == 0 {
		v3Contains(t, head, "Office")
	}
	if strings.Contains(head, "forged-agent") {
		t.Fatal("transcript reparsed body text as routing")
	}
}
func TestPairedTabTranscriptAcceptanceStoredRecipientsSurviveHistoryReload(t *testing.T) {
	for _, tc := range []struct {
		name  string
		route pairedTabRoute
	}{
		{"remote-only", pairedTabRoute{peer: []string{"remote-a"}}},
		{"mixed", pairedTabRoute{peer: []string{"remote-a"}, here: []string{"home-a"}, operator: true}},
		{"local-only-keeps-remote-operator", pairedTabRoute{here: []string{"home-a"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, key := pairedTabHarness(t)
			row := pairedTabStoredRow(t, key, tc.route)
			pairedTabLoadHistory(h, key, row)
			pairedTabAssertStoredHead(t, h, tc.route)
			// A changed directory and a fresh page cannot reinterpret persisted destinations.
			pairedTabLoadHistory(h, key, row)
			pairedTabAssertStoredHead(t, h, tc.route)
		})
	}
}
func pairedTabLoadHistory(h *chatHarness, key string, row protocol.ContactMessageView) {
	h.t.Helper()
	revision := h.m.State().Revision + 1
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventContactMessagesChanged, ContactKey: key, Watermark: protocol.Watermark{Epoch: "epoch", Revision: revision}}}
	req := h.take(protocol.TypeOperatorContact)
	page := contactMessagesPage(key, row)
	page.ContactPage.Directory = []string{"different-agent"}
	h.reply(req, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func pairedTabAssertOperator(t *testing.T, head string, wanted bool) {
	t.Helper()
	if !wanted {
		return
	}
	found := strings.Contains(head, "operator@here") || strings.Contains(strings.ToLower(head), "your operator")
	if !found {
		t.Errorf("stored local operator missing from recipients: %s", head)
	}
}
