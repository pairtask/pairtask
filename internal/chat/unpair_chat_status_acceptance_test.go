package chat_test

import (
	"encoding/json"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestUnpairChatStatusAcceptancePeerAndDeliveryLabelsAreActionable(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		fields     map[string]any
	}{
		{"paused", "paused by them", map[string]any{"peer_state": "paused"}},
		{"unpaired", "unpaired by them", map[string]any{"peer_state": "unpaired"}},
		{"older", "their state: unavailable (older version)", map[string]any{"peer_state_available": "unavailable"}},
		{"pending", "notice pending: they learn when they next connect", map[string]any{"notice": "pending"}},
		{"undelivered", "notice undelivered", map[string]any{"notice": "undelivered", "notice_attempts": 5}},
		{"another-pairing", "holds another pairing: /pair invite --repair", map[string]any{"summary": map[string]any{"sync": map[string]any{"last_failure": "other_pairing"}}}},
		{"unsupported", "unsupported: the other side must update", map[string]any{"summary": map[string]any{"sync": map[string]any{"last_failure": "unsupported"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, c := unpairChatHarness(t, unpairChatOptions{supported: true})
			wire, err := json.Marshal(tc.fields)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(wire, &c); err != nil {
				t.Fatal(err)
			}
			v2Command(h, "/pair status")
			h.reply(pairingChatRequest(h, "status"), unpairChatSnapshot(t, c))
			h.await(func() bool { return h.m.State().InFlight == 0 })
			v3Contains(t, ansi.Strip(h.m.View().Content), tc.want)
			h.input(chatKey(tea.KeyEscape, 0))
			unpairChatOpen(t, h, c)
			v3Contains(t, ansi.Strip(h.m.View().Content), tc.want)
		})
	}
}
func TestUnpairChatStatusAcceptanceRequestAndJoinKindsAreExplicit(t *testing.T) {
	for _, tc := range []struct {
		kind, want string
		ended      bool
	}{{"pair", "re-pair", true}, {"repair", "repair", false}, {"address_move", "address move", false}} {
		h, c := unpairChatHarness(t, unpairChatOptions{supported: true})
		row := map[string]any{"identity_key": c.IdentityKey, "name": "Office", "state": "pending_acceptance", "kind": tc.kind}
		join := map[string]any{"operation_id": "kind-join", "peer": c.IdentityKey, "name": "Office", "state": "awaiting_acceptance", "destination": "host:443", "kind": tc.kind}
		if tc.ended {
			row["repairs_unpaired_at"] = "2026-11-07T06:00:00Z"
			join["repairs_unpaired_at"] = "2026-11-07T06:00:00Z"
		}
		wire, err := json.Marshal(map[string]any{"type": "operator_pairing_result", "pairing_result": map[string]any{"requests": []any{row}, "requests_total": 1, "joins": []any{join}, "joins_total": 1}})
		if err != nil {
			t.Fatal(err)
		}
		var reply protocol.Message
		if err = json.Unmarshal(wire, &reply); err != nil {
			t.Fatal(err)
		}
		v2Command(h, "/pair status")
		h.reply(pairingChatRequest(h, "status"), reply)
		h.await(func() bool { return h.m.State().InFlight == 0 })
		v3Contains(t, ansi.Strip(h.m.View().Content), tc.want)
	}
}
