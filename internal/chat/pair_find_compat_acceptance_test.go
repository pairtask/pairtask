package chat_test

import (
	"strings"
	"testing"
)

func TestPairFindCompatibilityOlderBrokerRefusesBeforeOpeningAnInvitation(t *testing.T) {
	h := pairingChatHarness(t, true) // pairing, without the discovery capabilities
	v2Command(h, "/pair open --discoverable")
	if h.m.State().InFlight != 0 || len(h.requests) != 0 {
		t.Fatal("older broker received an open it could silently treat as private")
	}
	if !strings.Contains(strings.ToLower(h.m.State().Status), "update") {
		t.Fatalf("unsupported discoverable open did not explain how to proceed: %q", h.m.State().Status)
	}

	v2Command(h, "/pair find")
	if h.m.State().InFlight != 0 || len(h.requests) != 0 {
		t.Fatal("older broker received an unsupported discovery read")
	}
	if !strings.Contains(strings.ToLower(h.m.State().Status), "update") {
		t.Fatalf("unsupported find did not explain how to proceed: %q", h.m.State().Status)
	}
}
