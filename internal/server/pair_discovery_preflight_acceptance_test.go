//go:build linux

package server

import (
	"os"
	"testing"
)

func TestPairDiscoveryAcceptanceUnavailableHelperDoesNotReplacePrivateInvitation(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	private := pairingOpen(f)
	response := roomOperator(f, map[string]any{
		"type":                 "operator_pairing",
		"pairing_op":           "open",
		"pairing_discoverable": true,
	})
	if response.text("type") != "error" {
		t.Fatalf("discoverable open succeeded without a discovery helper: %s", sessionJSON(response))
	}
	pairingNoCode(t, response, private.Code)
	status := pairingSnapshot(t, pairingCommand(f, "status", ""))
	if status.Invitation == nil || status.Invitation.ID != private.ID {
		t.Fatal("failed discoverable open replaced the existing private invitation")
	}
}
