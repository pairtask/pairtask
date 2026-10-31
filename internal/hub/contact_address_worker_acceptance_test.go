package hub

import (
	"context"
	"database/sql"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

type blockedAddressProbe struct {
	started, release chan struct{}
}

func newBlockedAddressProbe(t *testing.T) *blockedAddressProbe {
	t.Helper()
	p := &blockedAddressProbe{started: make(chan struct{}, 1), release: make(chan struct{})}
	t.Cleanup(func() { addressRelease(p.release) })
	return p
}
func (p *blockedAddressProbe) dial(ctx context.Context, candidate string) (net.Conn, error) {
	if candidate != "new.example:7445" {
		return nil, context.DeadlineExceeded
	}
	p.started <- struct{}{}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.release:
		return nil, context.DeadlineExceeded
	}
}
func TestContactAddressAcceptanceBlockedProbeDoesNotBlockHub(t *testing.T) {
	f := newAddressFixture(t)
	f.joined(t)
	probe := newBlockedAddressProbe(t)
	f.run(t, probe.dial)
	answer := f.submit(f.request())
	addressAwait(t, probe.started)
	requireAddressPending(t, answer)
	requireCompetingAddressRefused(t, f)
	other := f.submit(protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpList, MessageID: "other"})
	messageSyncEqual(t, addressAwait(t, other).Type, protocol.TypeOperatorPairingResult)
	addressRelease(probe.release)
	messageSyncEqual(t, addressAwait(t, answer).Type, protocol.TypeError)
	messageSyncEqual(t, f.contact(t).Destination, "peer-1:443")
}
func requireAddressPending(t *testing.T, answer <-chan protocol.Message) {
	t.Helper()
	select {
	case premature := <-answer:
		t.Fatalf("probe answered before verification: %+v", premature)
	default:
	}
}
func requireCompetingAddressRefused(t *testing.T, f *addressFixture) {
	t.Helper()
	competing := f.request()
	competing.MessageID = "move-2"
	competing.ContactText = "another.example:7445"
	messageSyncEqual(t, addressAwait(t, f.submit(competing)).Type, protocol.TypeError)
}
func TestContactAddressAcceptanceShutdownCancelsProbeAndSettlesAudit(t *testing.T) {
	f := newAddressFixture(t)
	f.joined(t)
	probe := newBlockedAddressProbe(t)
	f.run(t, probe.dial)
	answer := f.submit(f.request())
	addressAwait(t, probe.started)
	f.cancel()
	addressAwait(t, f.h.Done())
	addressAwait(t, answer)
	messageSyncEqual(t, f.contact(t).Destination, "peer-1:443")
	requireAddressRefused(t, f)
	requireRejectedAddressAudit(t, f)
}
func requireAddressRefused(t *testing.T, f *addressFixture) {
	t.Helper()
	outcome, known, err := f.h.store.BeginContactAddress(context.Background(), store.AddressOp{
		ID: "move-1", Contact: f.key, Candidate: "new.example:7445", At: time.Now()})
	messageWorkerOK(t, err)
	messageSyncEqual(t, known, true)
	messageSyncEqual(t, outcome.State, store.AddressRefused)
}
func requireRejectedAddressAudit(t *testing.T, f *addressFixture) {
	t.Helper()
	raw, err := sql.Open("sqlite", strings.TrimSuffix(f.h.store.PeerDir(), ".peer"))
	messageWorkerOK(t, err)
	defer raw.Close()
	var rejected, pending int
	messageWorkerOK(t, raw.QueryRow(`SELECT count(*) FROM operator_audit WHERE action='contact address' AND message_id='move-1' AND result='rejected'`).Scan(&rejected))
	messageWorkerOK(t, raw.QueryRow(`SELECT count(*) FROM operator_audit WHERE action='contact address' AND message_id='move-1' AND result='pending'`).Scan(&pending))
	messageSyncEqual(t, rejected, 1)
	messageSyncEqual(t, pending, 0)
}
