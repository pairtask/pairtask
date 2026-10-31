package hub

import (
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestContactAddressAcceptanceSessionKeepsCorrelationUntilFinalAnswer(t *testing.T) {
	f := newAddressFixture(t)
	f.joined(t)
	probe := newBlockedAddressProbe(t)
	f.run(t, probe.dial)
	sess := NewOperatorSession(0)
	out := f.h.OpenOperatorSession(f.ctx, protocol.Message{Type: protocol.TypeOperatorSessionOpen, RequestID: "open"}, sess)
	messageSyncEqual(t, out.Type, protocol.TypeOperatorSession)
	request := f.request()
	request.RequestID = "address-request"
	messageSyncEqual(t, f.h.SessionRequest(f.ctx, sess, request), true)
	addressAwait(t, probe.started)
	other := protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpList, RequestID: "other-request"}
	messageSyncEqual(t, f.h.SessionRequest(f.ctx, sess, other), true)
	requireOtherSessionResponse(t, sess)
	addressRelease(probe.release)
	requireFinalAddressResponse(t, sess)
}
func requireOtherSessionResponse(t *testing.T, sess *OperatorSession) {
	t.Helper()
	out := sessionQueueNext(t, sess)
	messageSyncEqual(t, out.RequestID, "other-request")
	messageSyncEqual(t, out.Type, protocol.TypeOperatorPairingResult)
}
func requireFinalAddressResponse(t *testing.T, sess *OperatorSession) {
	t.Helper()
	out := sessionQueueNext(t, sess)
	messageSyncEqual(t, out.RequestID, "address-request")
	messageSyncEqual(t, out.MessageID, "move-1")
	messageSyncEqual(t, out.Type, protocol.TypeError)
	ctx, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	if extra, ok := sess.Next(ctx); ok {
		t.Fatalf("address request answered twice: %+v", extra)
	}
}
