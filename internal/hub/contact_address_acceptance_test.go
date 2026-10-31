package hub

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
)

// addressFixture owns one confirmed contact and the lifetime of its hub.
type addressFixture struct {
	h      *Hub
	key    string
	ctx    context.Context
	cancel context.CancelFunc
}

func newAddressFixture(t *testing.T) *addressFixture {
	t.Helper()
	h := newTestHub(t)
	messageSyncEqual(t, h.loadIdentity(context.Background()), "")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &addressFixture{h: h, ctx: ctx, cancel: cancel}
}

func (f *addressFixture) joined(t *testing.T) {
	t.Helper()
	f.key = messageWorkerContact(t, f.h, 1)
}

func (f *addressFixture) run(t *testing.T, dial func(context.Context, string) (net.Conn, error)) {
	t.Helper()
	f.h.SetPairing(PairingConfig{Clock: time.Now, Dial: dial})
	go f.h.Run(f.ctx)
	t.Cleanup(func() {
		f.cancel()
		addressAwait(t, f.h.Done())
	})
}

func (f *addressFixture) request() protocol.Message {
	return protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpAddress,
		ContactKey: f.key, ContactText: "new.example:7445", MessageID: "move-1"}
}

func (f *addressFixture) submit(msg protocol.Message) <-chan protocol.Message {
	answer := make(chan protocol.Message, 1)
	go func() { answer <- f.h.SubmitOperator(f.ctx, 0, msg) }()
	return answer
}

func (f *addressFixture) contact(t *testing.T) peer.Contact {
	t.Helper()
	c, found, err := f.h.store.Contact(context.Background(), f.key)
	messageWorkerOK(t, err)
	messageSyncEqual(t, found, true)
	return c
}

func addressAwait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("address operation did not complete within three seconds")
		var zero T
		return zero
	}
}

func addressRelease(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func addressContact(t *testing.T, h *Hub, key string) {
	t.Helper()
	ctx, at := context.Background(), time.Now()
	const op = "address-join"
	_, err := h.store.BeginJoin(ctx, peer.JoinStart{OperationID: op, Joiner: h.identity.Fingerprint(), Destination: "old.example:7445", At: at})
	messageWorkerOK(t, err)
	_, err = h.store.SetCorrelation(ctx, op, peer.InviterHello{InvitationID: "inv-address", OperationID: "remote-address"}, at)
	messageWorkerOK(t, err)
	r, err := h.store.RecordVerified(ctx, op, peer.ExchangeResult{Peer: key, TranscriptID: make([]byte, 64)}, at)
	messageWorkerOK(t, err)
	_, err = h.store.RecordReceipt(ctx, op, peer.JoinReceipt{InvitationID: r.InvitationID, InviterOperationID: r.InviterOperationID, Joiner: r.Joiner, TranscriptID: r.TranscriptID}, at)
	messageWorkerOK(t, err)
	_, err = h.store.RecordDecision(ctx, op, peer.PairingAccepted, at)
	messageWorkerOK(t, err)
	_, err = h.store.RecordDecisionConfirmed(ctx, op, peer.PairingAccepted, at)
	messageWorkerOK(t, err)
}
