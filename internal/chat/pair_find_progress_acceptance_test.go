package chat_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPairFindProgressExplainsWaitingBeforeFirstScan(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  string
	}{
		{"pending", "searching"},
		{"scanning", "nothing heard yet"},
		{"helper_off", "helper is off"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			h := findChat(t)
			v2Command(h, "/pair find")
			req := h.take(protocol.TypeOperatorContact)
			view := map[string]any{
				"scan_state": tc.state, "helper_seen_at": time.Now().UTC().Format(time.RFC3339),
				"invitations": []any{}, "devices": []any{},
			}
			if tc.state == "helper_off" {
				view["helper_seen_at"] = nil
			}
			wire, err := json.Marshal(map[string]any{"type": "operator_contact_result", "contact_find": view})
			if err != nil {
				t.Fatal(err)
			}
			h.reply(req, roomChatJSON(t, string(wire)))
			h.await(func() bool { return h.m.State().InFlight == 0 })
			shown := strings.ToLower(ansi.Strip(h.m.View().Content))
			if !strings.Contains(shown, tc.want) {
				t.Fatalf("%s view did not explain %q: %s", tc.state, tc.want, shown)
			}
			if tc.state == "pending" {
				assertNoPrematureFindResult(t, shown)
			}
			if strings.Contains(shown, "reports within seconds") {
				t.Fatalf("view promised a report before the helper has begun: %s", shown)
			}
		})
	}
}

func assertNoPrematureFindResult(t *testing.T, shown string) {
	t.Helper()
	for _, forbidden := range []string{"none heard", "nothing heard yet"} {
		if strings.Contains(shown, forbidden) {
			t.Fatalf("pending scan prematurely claimed an empty result: %s", shown)
		}
	}
}
