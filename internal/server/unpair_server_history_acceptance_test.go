//go:build linux

package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestUnpairHistoryOperatorAcceptanceListAndReadRemainLocal(t *testing.T) {
	f := newUnpairOperatorFixture(t)
	_, err := f.st.PeerMessages().Accept(context.Background(), f.key, messaging.Message{ID: "retained-message", N: 1, Text: "history", CreatedAt: time.Now()}, time.Now())
	operatorNoError(t, err)
	f.rootUnpair(t)
	out := f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpList, ContactKey: f.key})
	operatorEqual(t, "tombstone list allowed", out.Type, protocol.TypeOperatorContactResult)
	out = f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpRead, ContactKey: f.key, ContactRead: []string{"retained-message"}, MessageID: "local-history-read"})
	operatorEqual(t, "tombstone read allowed", out.Type, protocol.TypeOperatorContactResult)
	var receipt string
	operatorNoError(t, f.db.QueryRow(`SELECT receipt FROM peer_messages WHERE contact=? AND message_id='retained-message'`, f.key).Scan(&receipt))
	operatorEqual(t, "read stays local", receipt, "local")
}
func TestUnpairHistoryOperatorAcceptanceMutationsAreRefused(t *testing.T) {
	f := newUnpairOperatorFixture(t)
	f.rootUnpair(t)
	for _, op := range []string{"send", "ack", "share", "unshare", "name", "pause", "resume", "sync"} {
		req := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: op, ContactKey: f.key, MessageID: "blocked-" + op}
		switch op {
		case "send":
			req.ContactText = "no new traffic"
		case "ack":
			req.ContactAck = []string{"retained-message"}
		case "share", "unshare":
			req.ContactAgents = []string{"worker"}
		}
		if op == "sync" {
			req.MessageID = ""
		}
		out := f.send(req)
		operatorEqual(t, "unpaired refuses "+op, out.Code, "contact_unpaired")
	}
}
func TestUnpairHistoryOperatorAcceptanceSnapshotRetainsEpochAndLegacyState(t *testing.T) {
	f := newUnpairOperatorFixture(t)
	f.rootUnpair(t)
	snapshot := settleSnapshot(t, pairingCommand(f.operatorFixture, "status", ""))
	operatorEqual(t, "one retained contact", len(snapshot.Contacts), 1)
	wire := f.fields(t, protocol.Message{PairingResult: &snapshot})
	var view struct {
		UnpairAvailable bool `json:"unpair_available"`
		Contacts        []struct {
			Epoch      int64  `json:"pairing_epoch"`
			Legacy     bool   `json:"legacy"`
			UnpairedAt string `json:"unpaired_at"`
		} `json:"contacts"`
	}
	operatorNoError(t, json.Unmarshal(wire["pairing_result"], &view))
	operatorEqual(t, "unpair availability advertised", view.UnpairAvailable, true)
	operatorEqual(t, "retained epoch", view.Contacts[0].Epoch, int64(1))
	operatorEqual(t, "legacy pairing stayed legacy", view.Contacts[0].Legacy, true)
	operatorEqual(t, "tombstone timestamp visible", view.Contacts[0].UnpairedAt != "", true)
}
