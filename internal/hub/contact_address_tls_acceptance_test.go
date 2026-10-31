package hub

import (
	"context"
	"crypto/rand"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

type addressMarker struct{ fingerprint string }

func (m *addressMarker) IdentityFingerprint(context.Context) (string, bool, error) {
	return m.fingerprint, m.fingerprint != "", nil
}
func (m *addressMarker) InitIdentity(_ context.Context, fp string) error {
	m.fingerprint = fp
	return nil
}
func addressPeer(t *testing.T) *peer.WorkspaceIdentity {
	t.Helper()
	id, err := peer.LoadOrCreateIdentity(context.Background(), filepath.Join(t.TempDir(), "peer"), &addressMarker{}, rand.Reader)
	messageWorkerOK(t, err)
	return id
}

// TCP buffering avoids artificial simultaneous TLS alert write deadlocks.
type addressProbeServer struct {
	listener         net.Listener
	own, presented   *peer.WorkspaceIdentity
	done             chan error
	payload          chan []byte
	started, release chan struct{}
}

func newAddressProbeServer(t *testing.T, f *addressFixture, presented *peer.WorkspaceIdentity) *addressProbeServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	messageWorkerOK(t, err)
	s := &addressProbeServer{listener: listener, own: f.h.identity, presented: presented,
		done: make(chan error, 1), payload: make(chan []byte, 1), started: make(chan struct{}, 1), release: make(chan struct{})}
	t.Cleanup(func() { addressRelease(s.release); listener.Close() })
	go func() { s.done <- s.serve() }()
	return s
}
func (s *addressProbeServer) serve() error {
	raw, err := s.listener.Accept()
	if err != nil {
		return err
	}
	defer raw.Close()
	s.started <- struct{}{}
	<-s.release
	conn, err := firstcontact.ServerWith(context.Background(), raw, s.presented, time.Now,
		firstcontact.ServerOptions{MessagePins: func(fp string) bool { return fp == s.own.Fingerprint() }})
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	payload, _ := conn.MessageFrames(ctx).Recv()
	s.payload <- payload
	return nil
}
func (s *addressProbeServer) dial(ctx context.Context, candidate string) (net.Conn, error) {
	if candidate != "new.example:7445" {
		return nil, context.DeadlineExceeded
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", s.listener.Addr().String())
}
func (s *addressProbeServer) requireNoPayload(t *testing.T) {
	t.Helper()
	messageWorkerOK(t, addressAwait(t, s.done))
	payload := addressAwait(t, s.payload)
	if payload != nil {
		t.Fatalf("probe sent a message before changing address: %q", payload)
	}
}

func TestContactAddressAcceptanceTLSIdentityPinBeforeCommit(t *testing.T) {
	t.Run("wrong-key", func(t *testing.T) {
		f := newAddressFixture(t)
		id := addressPeer(t)
		f.key = id.Fingerprint()
		addressContact(t, f.h, f.key)
		server := newAddressProbeServer(t, f, addressPeer(t))
		addressRelease(server.release)
		f.run(t, server.dial)
		out := addressAwait(t, f.submit(f.request()))
		messageSyncEqual(t, out.Type, protocol.TypeError)
		messageSyncEqual(t, f.contact(t).Destination, "old.example:7445")
		addressAwait(t, server.done)
	})
	t.Run("same-key", testAddressSameKey)
}
func testAddressSameKey(t *testing.T) {
	f := newAddressFixture(t)
	id := addressPeer(t)
	f.key = id.Fingerprint()
	addressContact(t, f.h, f.key)
	_, err := f.h.store.PeerMessages().Queue(f.ctx, messaging.Outgoing{Contact: f.key, ID: "queued-before-probe", Text: "must not leave in probe", Now: time.Now()})
	messageWorkerOK(t, err)
	server := newAddressProbeServer(t, f, id)
	addressRelease(server.release)
	f.run(t, server.dial)
	out := addressAwait(t, f.submit(f.request()))
	messageSyncEqual(t, out.Type, protocol.TypeOperatorContactResult)
	if out.PairingResult == nil {
		t.Fatal("successful move omitted refreshed snapshot")
	}
	messageSyncEqual(t, f.contact(t).Destination, "new.example:7445")
	server.requireNoPayload(t)
}
func TestContactAddressAcceptanceNewerRedemptionBeatsVerifiedStaleProbe(t *testing.T) {
	f := newAddressFixture(t)
	id := addressPeer(t)
	f.key = id.Fingerprint()
	addressContact(t, f.h, f.key)
	server := newAddressProbeServer(t, f, id)
	f.run(t, server.dial)
	answer := f.submit(f.request())
	addressAwait(t, server.started)
	recordDestinationMove(t, f.h, f.key)
	addressRelease(server.release)
	out := addressAwait(t, answer)
	messageSyncEqual(t, out.Type, protocol.TypeError)
	messageSyncEqual(t, f.contact(t).Destination, "new-peer:443")
}
