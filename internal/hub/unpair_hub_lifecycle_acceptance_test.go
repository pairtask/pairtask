package hub

import (
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func TestUnpairHubLifecycleAcceptanceWakesNoticeAndReplayPreservesFreshLiveLink(t *testing.T) {
	w := newControlWorkerFixture(t)
	w.run(t)
	old, ok := w.h.claimLive(w.key, false)
	messageSyncEqual(t, ok, true)
	req := unpairHubRequest(t, w)
	out := addressAwait(t, w.submit(req))
	messageSyncEqual(t, out.Type, "operator_contact_result")
	select {
	case <-old.stop:
	default:
		t.Fatal("unpair did not close the old live link")
	}
	awaitMessageWorkerDial(t, w.started)
	var generation int64
	messageWorkerOK(t, w.raw.QueryRow(`SELECT generation FROM peer_contacts WHERE identity_key=?`, w.key).Scan(&generation))
	w.attempts(t, generation, 1)
	w.h.releaseLive(w.key, old)
	// A replay is a historical answer, even after this identity paired again.
	unpairHubReinstall(t, w)
	fresh, ok := w.h.claimLive(w.key, false)
	messageSyncEqual(t, ok, true)
	defer w.h.releaseLive(w.key, fresh)
	out = addressAwait(t, w.submit(req))
	messageSyncEqual(t, out.Type, "operator_contact_result")
	select {
	case <-fresh.stop:
		t.Fatal("historical unpair replay closed the new pairing's link")
	default:
	}
}
func TestUnpairHubLifecycleAcceptanceNamedRetiredJoinIsNeverQueued(t *testing.T) {
	w := newControlWorkerFixture(t)
	w.unpair(t)
	ids, rerr := w.h.settleTargets(context.Background(), "join-1")
	if rerr == nil {
		t.Fatal("named retired join was admitted for network settlement")
	}
	messageSyncEqual(t, len(ids), 0)
}
func TestUnpairHubLifecycleAcceptanceNamedRefusedMoveIsNeverQueued(t *testing.T) {
	w := newControlWorkerFixture(t)
	ctx := context.Background()
	at := time.Now()
	_, err := w.h.store.BeginJoin(ctx, peer.JoinStart{OperationID: "refused-move", Joiner: w.h.identity.Fingerprint(), Name: "Local", Destination: "new.example:7445", At: at})
	messageWorkerOK(t, err)
	_, err = w.h.store.SetCorrelation(ctx, "refused-move", peer.InviterHello{InvitationID: "move-invitation", OperationID: "move-inviter"}, at)
	messageWorkerOK(t, err)
	_, err = w.h.store.RecordVerified(ctx, "refused-move", peer.ExchangeResult{Peer: w.key, Mode: peer.ModeEpoch, TranscriptID: []byte("address-move-transcript")}, at)
	messageWorkerOK(t, err)
	ids, rerr := w.h.settleTargets(ctx, "refused-move")
	if rerr == nil {
		t.Fatal("named refused ledger exchange was admitted for network settlement")
	}
	messageSyncEqual(t, len(ids), 0)
}
func TestUnpairHubLifecycleAcceptanceOtherPairingIsActionableClass(t *testing.T) {
	messageSyncEqual(t, classifySync(messaging.ErrOtherPairing), "other_pairing")
}
