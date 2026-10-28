package chat_test

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestRedeemChatAcceptanceSecretClearedOnSuccessAndRefusal(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "refusal"}[refused], func(t *testing.T) {
			h := pairingChatHarness(t, true)
			v2Type(h, "unrelated draft")
			v2Command(h, "/pair redeem host:443 "+pairingTestCode+" Bob")
			req := pairingChatRequest(h, "redeem")
			fields := pairingChatFields(t, req)
			chatEqual(t, "destination", fields["pairing_destination"], any("host:443"))
			chatEqual(t, "secret on direct request", strings.ReplaceAll(fields["pairing_code"].(string), "-", ""), "739281")
			pairingChatNoCode(h)
			reply := roomChatJSON(t, `{"type":"operator_pairing_result","pairing_join":{"operation_id":"join-1","state":"intent","destination":"host:443","name":"Bob"},"pairing_result":{"requests":[],"requests_total":0,"joins":[{"operation_id":"join-1","state":"intent","destination":"host:443","name":"Bob"}],"joins_total":1,"redeem_available":true}}`)
			if refused {
				reply = roomChatJSON(t, `{"type":"error","code":"pairing_refused","detail":"redeem_busy"}`)
			}
			h.reply(req, reply)
			h.await(func() bool { return h.m.State().InFlight == 0 })
			pairingChatNoCode(h)
			chatEqual(t, "no transcript entry", len(h.m.State().Entries), 0)
			chatEqual(t, "draft preserved without code", h.m.State().Draft, "unrelated draft")
			h.input(chatKey('k', tea.ModCtrl))
			h.input(chatKey(tea.KeyUp, 0))
			pairingChatNoCode(h)
		})
	}
}

func TestRedeemChatAcceptanceInvalidInputClearsSecret(t *testing.T) {
	for _, command := range []string{"/pair redeem missing-port " + pairingTestCode, "/pair redeem host:443 " + pairingTestCode + " too many names"} {
		h := pairingChatHarness(t, true)
		v2Command(h, command)
		pairingChatNoCode(h)
		chatEqual(t, "no invalid request", h.m.State().InFlight, 0)
		chatEqual(t, "no invalid queue", h.m.State().Queued, 0)
	}
}

func TestRedeemChatAcceptanceOldBrokerRefusedWithoutSecret(t *testing.T) {
	caps := slices.DeleteFunc(slices.Clone(protocol.SessionCapabilities), func(cap string) bool { return cap == "pairing_redeem" })
	h := pairingChatCapabilities(t, caps)
	v2Command(h, "/pair redeem host:443 "+pairingTestCode)
	chatEqual(t, "old broker sends nothing", h.m.State().InFlight, 0)
	chatEqual(t, "old broker queues nothing", h.m.State().Queued, 0)
	pairingChatNoCode(h)
	view := strings.ToLower(ansi.Strip(h.m.View().Content))
	v3Contains(t, view, "support")
}

func TestRedeemChatAcceptanceDisconnectNeverReplaysSecret(t *testing.T) {
	h := pairingChatHarness(t, true)
	v2Command(h, "/pair redeem host:443 "+pairingTestCode)
	pairingChatRequest(h, "redeem")
	h.reconnectTo(newChatScript(), "epoch")
	h.take(protocol.TypeOperatorSnapshot)
	pairingChatNoCode(h)
	chatEqual(t, "no redeem chat entry", len(h.m.State().Entries), 0)
}

func TestRedeemChatAcceptanceDisconnectClearsUnsubmittedCode(t *testing.T) {
	h := pairingChatHarness(t, true)
	v2Type(h, "unrelated draft")
	h.input(chatKey('k', tea.ModCtrl))
	v2Type(h, "pair redeem host:443 "+pairingTestCode)
	h.reconnectTo(newChatScript(), "epoch")
	h.take(protocol.TypeOperatorSnapshot)
	pairingChatNoCode(h)
	chatEqual(t, "unrelated draft preserved", h.m.State().Draft, "unrelated draft")
}
