package hub

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

type addressDialCounter struct {
	candidate string
	calls     atomic.Int32
}

func (c *addressDialCounter) dial(_ context.Context, candidate string) (net.Conn, error) {
	if candidate == c.candidate {
		c.calls.Add(1)
	}
	return nil, context.DeadlineExceeded
}
func TestContactAddressAcceptanceUsesActualDialElection(t *testing.T) {
	f := newAddressFixture(t)
	f.joined(t)
	counter := &addressDialCounter{candidate: "new.example:7445"}
	f.run(t, counter.dial)
	f.invited(t)
	c := f.contact(t)
	if c.Destination == "" {
		t.Fatal("election fixture must still have an address")
	}
	messageSyncEqual(t, dials(c, f.h.identity.Fingerprint()), false)
	out := addressAwait(t, f.submit(f.request()))
	messageSyncEqual(t, out.Type, protocol.TypeError)
	messageSyncEqual(t, counter.calls.Load(), int32(0))
}
func (f *addressFixture) invited(t *testing.T) {
	t.Helper()
	outcome := peer.PairingOutcome{OperationID: "invited-address", InvitationID: "invite-address", IdentityKey: f.key,
		Name: "Peer", TranscriptID: make([]byte, 64), At: time.Now()}
	_, err := f.h.store.Commit(f.ctx, outcome)
	messageWorkerOK(t, err)
	_, err = f.h.store.Accept(f.ctx, f.key)
	messageWorkerOK(t, err)
	_, err = f.h.store.Deliver(f.ctx, f.key, peer.PairingAccepted, time.Now())
	messageWorkerOK(t, err)
}
func TestContactAddressAcceptanceReplayShowsCurrentDestinationWithoutReprobe(t *testing.T) {
	f := newAddressFixture(t)
	f.joined(t)
	f.acceptMove(t, store.AddressOp{ID: "address-A", Contact: f.key, Candidate: "first.example:7445", At: time.Now()})
	f.acceptMove(t, store.AddressOp{ID: "address-B", Contact: f.key, Candidate: "second.example:7445", At: time.Now()})
	counter := &addressDialCounter{candidate: "first.example:7445"}
	f.run(t, counter.dial)
	request := f.request()
	request.MessageID, request.ContactText = "address-A", "first.example:7445"
	out := addressAwait(t, f.submit(request))
	requireCurrentAddressSnapshot(t, out)
	messageSyncEqual(t, counter.calls.Load(), int32(0))
}
func (f *addressFixture) acceptMove(t *testing.T, op store.AddressOp) {
	t.Helper()
	before := f.contact(t)
	_, known, err := f.h.store.BeginContactAddress(f.ctx, op)
	messageWorkerOK(t, err)
	messageSyncEqual(t, known, false)
	_, err = f.h.store.ChangeContactAddress(f.ctx, store.ContactAddress{OpID: op.ID, Contact: f.key, From: before.Destination,
		FromAt: before.DestinationAt, To: op.Candidate, At: time.Now(), StillDials: func(peer.Contact) bool { return true }})
	messageWorkerOK(t, err)
}
func requireCurrentAddressSnapshot(t *testing.T, out protocol.Message) {
	t.Helper()
	messageSyncEqual(t, out.Type, protocol.TypeOperatorContactResult)
	if out.PairingResult == nil {
		t.Fatal("replay omitted current snapshot")
	}
	messageSyncEqual(t, len(out.PairingResult.Contacts), 1)
	messageSyncEqual(t, out.PairingResult.Contacts[0].Destination, "second.example:7445")
}
