package chat_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPairFindCompatibilityOlderBrokerRefusesBeforeOpeningAnInvitation(t *testing.T) {
	caps := slices.DeleteFunc(slices.Clone(protocol.SessionCapabilities), func(cap string) bool {
		return cap == protocol.CapabilityPairingDiscoverable || cap == protocol.CapabilityContactFind
	})
	h := pairingChatCapabilities(t, caps) // pairing, without the discovery capabilities
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
