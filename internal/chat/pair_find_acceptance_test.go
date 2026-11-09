package chat_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func findChat(t *testing.T) *chatHarness {
	t.Helper()
	caps := slices.Clone(protocol.SessionCapabilities)
	return pairingChatCapabilities(t, caps)
}

func TestPairFindAcceptanceDiscoverableOpenIsExplicit(t *testing.T) {
	h := findChat(t)
	v2Command(h, "/pair open --discoverable")
	req := pairingChatRequest(h, "open")
	if pairingChatFields(t, req)["pairing_discoverable"] != true {
		t.Fatalf("discoverable open did not request public invitation: %+v", pairingChatFields(t, req))
	}
	h.reply(req, pairingChatReply(t, pairingTestCode, nil, 0))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	if len(h.m.State().Entries) != 0 {
		t.Fatal("opening a discoverable invitation wrote a chat message")
	}
	if !strings.Contains(ansi.Strip(h.m.View().Content), pairingTestCode) {
		t.Fatal("invitation code was not shown in the private panel")
	}
}

func TestPairFindAcceptancePrivateOpenDoesNotRequestDiscovery(t *testing.T) {
	h := findChat(t)
	v2Command(h, "/pair open")
	req := pairingChatRequest(h, "open")
	if pairingChatFields(t, req)["pairing_discoverable"] == true {
		t.Fatal("plain /pair open unexpectedly publishes an invitation")
	}
}

func TestPairFindAcceptancePickerLabelsUnverifiedTailnetAndHoldsRedeem(t *testing.T) {
	h := findChat(t)
	v2Command(h, "/pair find")
	req := h.take(protocol.TypeOperatorContact)
	if req.ContactOp != "find" {
		t.Fatalf("find used contact op %q", req.ContactOp)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	response, err := json.Marshal(map[string]any{
		"type": "operator_contact_result",
		"contact_find": map[string]any{
			"helper_seen_at": now,
			"invitations":    []map[string]any{{"name": "Office Mac", "host": "192.168.8.20", "port": 8445, "seen_at": now}},
			"devices":        []map[string]any{{"name": "work.tailnet.ts.net", "address": "100.70.1.2"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.reply(req, roomChatJSON(t, string(response)))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := ansi.Strip(h.m.View().Content)
	for _, want := range []string{"Open invitations on this network", "Office Mac", "192.168.8.20:8445", "Tailnet devices (unverified)", "work.tailnet.ts.net"} {
		v3Contains(t, view, want)
	}
	if len(h.m.State().Entries) != 0 || h.m.State().InFlight != 0 {
		t.Fatal("viewing discovery results sent a message or started pairing")
	}
	commandCompleteItems(t, h, []string{"192.168.8.20:8445", "work.tailnet.ts.net:7445", "100.70.1.2:7445"})
	h.input(chatKey(tea.KeyEnter, 0))
	if got := h.m.State().CommandLine.Text; !strings.Contains(got, "pair redeem 192.168.8.20:8445 ") {
		t.Fatalf("choosing LAN invitation should fill, not send, redeem command: %q", got)
	}
	if h.m.State().InFlight != 0 {
		t.Fatal("selecting a discovery row started pairing without the code")
	}
}
