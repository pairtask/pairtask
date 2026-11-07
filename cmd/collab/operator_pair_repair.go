package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
)

// A declared repair (#276): open and redeem may name the contact the
// pairing repairs, by its full identity key. It goes only to a broker that
// serves it, asked first, before a redemption's code is even read.

// repairArgument: --repair is open's and redeem's alone, and names a
// contact's full identity key.
func repairArgument(op, key string) error {
	if key == "" {
		return nil
	}
	if op != protocol.PairingOpOpen && op != protocol.PairingOpRedeem {
		return errors.New("--repair is only for pair open and pair redeem")
	}
	if peer.ValidIdentityKey(key) != nil {
		return errors.New("--repair takes the contact's full identity key: sha256: and 64 lowercase hex digits")
	}
	return nil
}

// repairServed: a declared repair goes only to a broker that serves it.
func repairServed(repair string, snapshot *protocol.PairingSnapshot, stderr io.Writer) bool {
	if repair == "" || snapshot.RepairAvailable {
		return true
	}
	fmt.Fprintln(stderr, "this broker can't declare a repair; upgrade it; nothing sent")
	return false
}

// probeRepair asks the broker before an open declaring a repair; one
// without it is refused here, before anything changes.
func probeRepair(ctx context.Context, call operatorCall, req protocol.Message, stderr io.Writer) bool {
	if req.PairingRepair == "" {
		return true
	}
	probe := protocol.Message{Type: protocol.TypeOperatorPairing, MessageID: uuid.NewString(), PairingOp: protocol.PairingOpStatus}
	resp, err := call.expect(ctx, probe, expectation{protocol.TypeOperatorPairingResult, "operator pair status"}, stderr)
	if err != nil {
		return false
	}
	if resp.PairingResult == nil {
		fmt.Fprintln(stderr, "operator pair: the broker sent no pairing state")
		return false
	}
	return repairServed(req.PairingRepair, resp.PairingResult, stderr)
}
