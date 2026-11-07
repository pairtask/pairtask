package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
)

func unpairHubRequest(t *testing.T, w *controlWorkerFixture) protocol.Message {
	t.Helper()
	wire, err := json.Marshal(map[string]any{"type": "operator_contact", "contact_op": "unpair", "contact_key": w.key, "contact_confirm": w.key, "contact_epoch": 1, "message_id": "lifecycle-unpair"})
	messageWorkerOK(t, err)
	var m protocol.Message
	messageWorkerOK(t, json.Unmarshal(wire, &m))
	return m
}
func unpairHubReinstall(t *testing.T, w *controlWorkerFixture) {
	t.Helper()
	ctx := context.Background()
	mark, err := w.h.store.PairingMark(ctx)
	messageWorkerOK(t, err)
	o := peer.PairingOutcome{OperationID: "lifecycle-reinstall", InvitationID: "lifecycle-invite", IdentityKey: w.key, Name: "Peer", Mode: peer.ModeEpoch, BegunMark: mark, TranscriptID: bytes.Repeat([]byte{42}, 64), At: time.Now()}
	_, err = w.h.store.Commit(ctx, o)
	messageWorkerOK(t, err)
	_, err = w.h.store.Accept(ctx, w.key)
	messageWorkerOK(t, err)
	_, err = w.h.store.DeliverExchange(ctx, peer.ExchangeDelivery{IdentityKey: w.key, OperationID: o.OperationID, Decision: peer.PairingAccepted, At: time.Now()})
	messageWorkerOK(t, err)
}
