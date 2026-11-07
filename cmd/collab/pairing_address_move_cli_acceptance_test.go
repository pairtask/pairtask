package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPairingAddressMoveCLIAcceptanceReportsSuccessWithoutFurtherAcceptance(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		var stdout, stderr bytes.Buffer
		join := protocol.PairingJoinView{OperationID: "address-move", State: "address_moved", Kind: "address_move"}
		code := reportRedeem(&stdout, &stderr, asJSON, join)
		assertStatusEqual(t, stderr.String(), code, 0)
		guidance := strings.ToLower(stderr.String())
		assertStatusEqual(t, "address update explained", strings.Contains(guidance, "address"), true)
		assertStatusEqual(t, "existing pairing preserved", strings.Contains(guidance, "pairing kept"), true)
		assertStatusEqual(t, "no fictitious pending acceptance", strings.Contains(guidance, "awaits"), false)
		assertStatusEqual(t, "not still exchanging", strings.Contains(guidance, "still exchanging"), false)
	}
}

func TestPairingAddressMoveCLIAcceptanceSettleIsAlreadyComplete(t *testing.T) {
	join := protocol.PairingJoinView{OperationID: "address-move", State: "address_moved", Kind: "address_move"}
	assertStatusEqual(t, "no polling for a decision", settleDone(join, ""), true)
	var stdout, stderr bytes.Buffer
	code := reportSettled(&stdout, &stderr, false, join)
	assertStatusEqual(t, stderr.String(), code, 0)
	assertStatusEqual(t, "no fictitious decision confirmation", strings.Contains(stdout.String(), "(confirmed)"), false)
}
