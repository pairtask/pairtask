//go:build linux

package server

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestInstallLiveAcceptanceInviterInstallClosesOldLinkButReplayKeepsFresh(t *testing.T) {
	f := newInstallLiveFixture(t)
	old := f.live(t)
	o := f.outcome(t, "inviter-live-repair", 22)
	o.Repair = f.remote.Fingerprint()
	_, err := f.f.st.Commit(context.Background(), o)
	operatorNoError(t, err)
	_, err = f.f.st.Accept(context.Background(), o.IdentityKey)
	operatorNoError(t, err)
	f.settleInviter(t, o)
	installLiveClosed(t, old)
	fresh := f.live(t)
	f.settleInviter(t, o)
	select {
	case <-fresh:
		t.Fatal("install replay closed the fresh pairing's live link")
	case <-time.After(80 * time.Millisecond):
	}
}
func TestInstallLiveAcceptanceJoinerConfirmedInstallClosesOldLink(t *testing.T) {
	f := newInstallLiveFixture(t)
	old := f.live(t)
	listener, served := f.joinerServer(t)
	ctx := context.Background()
	at := time.Now()
	_, err := f.f.st.BeginJoin(ctx, peer.JoinStart{OperationID: "joiner-live-repair", Joiner: f.ep.Identity.Fingerprint(), Name: "Local", Destination: listener.Addr().String(), Repair: f.remote.Fingerprint(), At: at})
	operatorNoError(t, err)
	_, err = f.f.st.SetCorrelation(ctx, "joiner-live-repair", peer.InviterHello{InvitationID: "joiner-live-inv", OperationID: "remote-live-op"}, at)
	operatorNoError(t, err)
	row, err := f.f.st.RecordVerified(ctx, "joiner-live-repair", peer.ExchangeResult{Peer: f.remote.Fingerprint(), TranscriptID: bytes.Repeat([]byte{33}, 64), Mode: peer.ModeEpoch}, at)
	operatorNoError(t, err)
	_, err = f.f.st.RecordReceipt(ctx, row.OperationID, peer.JoinReceipt{InvitationID: row.InvitationID, InviterOperationID: row.InviterOperationID, Joiner: row.Joiner, TranscriptID: row.TranscriptID}, at)
	operatorNoError(t, err)
	out := f.f.send(protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpSettle, PairingTarget: row.OperationID, MessageID: "install-live-settle"})
	operatorEqual(t, "settlement queued", out.Type, protocol.TypeOperatorPairingResult)
	select {
	case err := <-served:
		operatorNoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("joiner never reached its pinned settlement peer")
	}
	installLiveClosed(t, old)
}
