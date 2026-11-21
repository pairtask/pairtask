package chat_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPairedClaimsChatAcceptanceRemoteClaimsKeepWorkspaceAndState(t *testing.T) {
	var view protocol.ContactMessageView
	err := json.Unmarshal([]byte(`{"id":"claim-chat","cursor":1,"direction":"out","state":"delivered","text":"investigate","claims":[{"message_id":"claim-chat","task":"investigate","agent_id":"worker","role":"owner","state":"active","remote":{"workspace":"remote-workspace"}}],"claims_total":1,"claims_truncated":false}`), &view)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := contactMessagesHarness(t, view)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	visible := ansi.Strip(h.m.View().Content)
	for _, want := range []string{"worker@", "owner", "active"} {
		if !strings.Contains(visible, want) {
			t.Fatalf("missing %q in %s", want, visible)
		}
	}
}

func TestPairedClaimsChatAcceptanceRemoteControlBytesCannotReachTerminal(t *testing.T) {
	v := contactLayoutRow("control-claim", 1)
	v.Direction = "out"
	v.MessageClaims = &protocol.MessageClaims{Claims: []protocol.Claim{{Task: "investigate", AgentID: "worker\x1b[2J\a\r\nother", Role: "owner", State: "active", ClaimedAt: contactLayoutAt, Remote: &protocol.ClaimRemote{Workspace: "peer-key"}}}, Total: 1}
	h := contactLayoutHarness(t, v)
	raw := h.m.View().Content
	if strings.Contains(raw, "\x1b[2J") || strings.Contains(raw, "\a") {
		t.Fatal("remote control bytes reached the terminal")
	}
	visible := ansi.Strip(raw)
	for _, want := range []string{"last reported", "2026-11-01", "worker"} {
		if !strings.Contains(visible, want) {
			t.Fatalf("missing %q in %s", want, visible)
		}
	}
}
