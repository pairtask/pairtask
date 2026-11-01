package main

import (
	"fmt"
	"io"

	"github.com/pairtask/pairtask/internal/protocol"
)

// pauseOps are the operations that pause or resume a contact (#276).
var pauseOps = map[string]bool{protocol.ContactOpPause: true, protocol.ContactOpResume: true}

// pauseServed: a pause or resume goes only to a broker that serves it; an
// older one is refused here, before anything is sent.
func pauseServed(req protocol.Message, snapshot *protocol.PairingSnapshot, stderr io.Writer) bool {
	if !pauseOps[req.ContactOp] || snapshot.PauseAvailable {
		return true
	}
	fmt.Fprintf(stderr, "operator peer %s: this broker can't pause a workspace; upgrade it; nothing sent\n", req.ContactOp)
	return false
}

// opServed: an operation goes only to a broker that serves it; an older
// one is refused here, before anything is sent.
func opServed(req protocol.Message, snapshot *protocol.PairingSnapshot, stderr io.Writer) bool {
	return namesServed(req, snapshot, stderr) && pauseServed(req, snapshot, stderr)
}

// writeSnapshotResult says what an operation answered with a pairing
// snapshot changed: a name, or a pause.
func writeSnapshotResult(w io.Writer, resp protocol.Message) {
	if resp.PairingResult == nil {
		return
	}
	switch {
	case resp.ContactOp == protocol.ContactOpName:
		writeContactName(w, resp.ContactKey, resp.PairingResult.Contacts)
	case pauseOps[resp.ContactOp]:
		writeContactPause(w, resp.ContactKey, resp.PairingResult.Contacts)
	}
}

// writeContactPause says whether traffic with the contact is paused now.
func writeContactPause(w io.Writer, key string, contacts []protocol.PairingContactView) {
	for _, c := range contacts {
		if c.IdentityKey != key {
			continue
		}
		if c.PausedAt != nil {
			fmt.Fprintf(w, "Paused %s: new traffic is blocked until resume\n", key)
			return
		}
		fmt.Fprintf(w, "Resumed %s: queued messages can flow again\n", key)
		return
	}
}
