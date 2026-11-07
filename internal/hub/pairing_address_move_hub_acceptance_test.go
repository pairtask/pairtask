package hub

import (
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestPairingAddressMoveHubAcceptanceCompletedMoveNeverQueuesSettlement(t *testing.T) {
	w := newControlWorkerFixture(t)
	ctx, at := context.Background(), time.Now()
	_, err := w.h.store.BeginJoin(ctx, peer.JoinStart{OperationID: "completed-move", Joiner: w.h.identity.Fingerprint(), Name: "Local", Destination: "new.example:7445", At: at})
	messageWorkerOK(t, err)
	_, err = w.h.store.SetCorrelation(ctx, "completed-move", peer.InviterHello{InvitationID: "move-invitation", OperationID: "move-inviter"}, at)
	messageWorkerOK(t, err)
	r, err := w.h.store.RecordVerified(ctx, "completed-move", peer.ExchangeResult{Peer: w.key, Mode: peer.ModeEpoch, TranscriptID: []byte("address-move-transcript")}, at)
	messageWorkerOK(t, err)
	r, err = w.h.store.RecordReceipt(ctx, r.OperationID, peer.JoinReceipt{InvitationID: r.InvitationID, InviterOperationID: r.InviterOperationID, Joiner: r.Joiner, TranscriptID: r.TranscriptID}, at)
	messageWorkerOK(t, err)
	messageSyncEqual(t, string(r.State), "address_moved")
	messageSyncEqual(t, settleCandidate(r), false)
	ids, rerr := w.h.settleTargets(ctx, r.OperationID)
	if rerr != nil {
		t.Fatal(rerr)
	}
	messageSyncEqual(t, len(ids), 0)
}
