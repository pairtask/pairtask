package hub

import (
	"context"
	"crypto/rand"
	"database/sql"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
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
	if err != nil {
		t.Fatal(err)
	}
	return id
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

func TestContactAddressAcceptanceTLSIdentityPinBeforeCommit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		wrong bool
	}{
		{"wrong-key", true}, {"same-key", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addressProbeCase(t, tc.wrong)
		})
	}
}

type addressProbeServer struct {
	listener net.Listener
	done     chan error
	payload  chan []byte
}

func startAddressProbeServer(t *testing.T, own, presented *peer.WorkspaceIdentity) addressProbeServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	s := addressProbeServer{listener: listener, done: make(chan error, 1), payload: make(chan []byte, 1)}
	go s.serve(own, presented)
	return s
}

func (s addressProbeServer) serve(own, presented *peer.WorkspaceIdentity) {
	server, err := s.listener.Accept()
	if err == nil {
		var conn *firstcontact.Conn
		conn, err = firstcontact.ServerWith(context.Background(), server, presented, time.Now, firstcontact.ServerOptions{
			MessagePins: func(fp string) bool { return fp == own.Fingerprint() },
		})
		if err == nil {
			readCtx, stop := context.WithTimeout(context.Background(), time.Second)
			payload, _ := conn.MessageFrames(readCtx).Recv()
			s.payload <- payload
			stop()
			conn.Close()
		}
	}
	s.done <- err
}

func (s addressProbeServer) dial(ctx context.Context, destination string) (net.Conn, error) {
	if destination != "new.example:7445" {
		return nil, context.DeadlineExceeded
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", s.listener.Addr().String())
}

func addressProbeCase(t *testing.T, wrong bool) {
	t.Helper()
	h := newTestHub(t)
	if got := h.loadIdentity(context.Background()); got != "" {
		t.Fatalf("load identity: %s", got)
	}
	contactID := addressPeer(t)
	presented := contactID
	if wrong {
		presented = addressPeer(t)
	}
	addressContact(t, h, contactID.Fingerprint())
	_, err := h.store.PeerMessages().Queue(context.Background(), messaging.Outgoing{
		Contact: contactID.Fingerprint(), ID: "queued-before-probe", Text: "must not leave in probe", Now: time.Now()})
	messageWorkerOK(t, err)
	server := startAddressProbeServer(t, h.identity, presented)
	h.SetPairing(PairingConfig{Clock: time.Now, Dial: server.dial})
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-h.Done() }()
	go h.Run(ctx)
	probe, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	out := h.SubmitOperator(probe, 0, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpAddress,
		ContactKey: contactID.Fingerprint(), ContactText: "new.example:7445", MessageID: "move-pin"})
	contact, found, err := h.store.Contact(context.Background(), contactID.Fingerprint())
	if err != nil || !found {
		t.Fatalf("contact unavailable after probe: found=%v err=%v", found, err)
	}
	assertAddressProbe(t, wrong, out, contact, server)
}

func assertAddressProbe(t *testing.T, wrong bool, out protocol.Message, contact peer.Contact, server addressProbeServer) {
	t.Helper()
	if wrong {
		if out.Type != protocol.TypeError || contact.Destination != "old.example:7445" {
			t.Fatalf("wrong identity changed address: out=%+v contact=%+v", out, contact)
		}
	} else if out.Type != protocol.TypeOperatorContactResult || out.PairingResult == nil || contact.Destination != "new.example:7445" {
		t.Fatalf("same identity failed to move and refresh snapshot: out=%+v contact=%+v", out, contact)
	}
	select {
	case <-server.done:
	case <-time.After(2 * time.Second):
		t.Fatal("TLS probe left server blocked")
	}
	if !wrong {
		select {
		case payload := <-server.payload:
			if payload != nil {
				t.Fatalf("probe sent a message before changing address: %q", payload)
			}
		default:
			t.Fatal("successful probe did not check for premature messages")
		}
	}
}

func TestContactAddressAcceptanceNewerRedemptionBeatsVerifiedStaleProbe(t *testing.T) {
	h := newTestHub(t)
	if got := h.loadIdentity(context.Background()); got != "" {
		t.Fatalf("load identity: %s", got)
	}
	peerID := addressPeer(t)
	key := peerID.Fingerprint()
	addressContact(t, h, key)
	started, release := make(chan struct{}, 1), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		server, err := listener.Accept()
		if err != nil {
			return
		}
		started <- struct{}{}
		<-release
		conn, err := firstcontact.ServerWith(context.Background(), server, peerID, time.Now,
			firstcontact.ServerOptions{MessagePins: func(fp string) bool { return fp == h.identity.Fingerprint() }})
		if err == nil {
			conn.Close()
		}
	}()
	h.SetPairing(PairingConfig{Clock: time.Now, Dial: func(ctx context.Context, destination string) (net.Conn, error) {
		if destination != "candidate.example:7445" {
			return nil, context.DeadlineExceeded
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-h.Done() }()
	go h.Run(ctx)
	out := make(chan protocol.Message, 1)
	go func() {
		out <- h.SubmitOperator(ctx, 0, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpAddress,
			ContactKey: key, ContactText: "candidate.example:7445", MessageID: "stale-probe"})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("candidate probe never dialed")
	}
	// A separate, newer pairing changes the same identity's destination.
	recordDestinationMove(t, h, key)
	close(release)
	select {
	case answer := <-out:
		if answer.Type != protocol.TypeError {
			t.Fatalf("stale probe reported success: %+v", answer)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stale probe never settled")
	}
	contact, found, err := h.store.Contact(context.Background(), key)
	if err != nil || !found || contact.Destination != "new-peer:443" {
		t.Fatalf("stale probe overwrote newer pairing: %+v found=%v err=%v", contact, found, err)
	}
}

func TestContactAddressAcceptanceUsesActualDialElection(t *testing.T) {
	h := newTestHub(t)
	if got := h.loadIdentity(context.Background()); got != "" {
		t.Fatalf("load identity: %s", got)
	}
	key := messageWorkerContact(t, h, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-h.Done() }()
	var dialed atomic.Bool
	h.SetPairing(PairingConfig{Clock: time.Now, Dial: func(_ context.Context, destination string) (net.Conn, error) {
		if destination == "new.example:7445" {
			dialed.Store(true)
		}
		return nil, context.DeadlineExceeded
	}})
	go h.Run(ctx)
	// The joined side normally dials. A reciprocal invitation makes both
	// sides hold an address; the higher fingerprint must now serve instead.
	outcome := peer.PairingOutcome{OperationID: "invited-address", InvitationID: "invite-address", IdentityKey: key,
		Name: "Peer", TranscriptID: make([]byte, 64), At: time.Now()}
	_, err := h.store.Commit(ctx, outcome)
	messageWorkerOK(t, err)
	_, err = h.store.Accept(ctx, key)
	messageWorkerOK(t, err)
	_, err = h.store.Deliver(ctx, key, peer.PairingAccepted, time.Now())
	messageWorkerOK(t, err)
	contact, found, err := h.store.Contact(ctx, key)
	messageWorkerOK(t, err)
	if !found || contact.Destination == "" || dials(contact, h.identity.Fingerprint()) {
		t.Fatalf("test precondition: expected serving contact with address, got %+v", contact)
	}
	out := h.SubmitOperator(ctx, 0, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpAddress,
		ContactKey: key, ContactText: "new.example:7445", MessageID: "move-serving"})
	if out.Type != protocol.TypeError || dialed.Load() {
		t.Fatalf("serving side tried to probe: out=%+v dialed=%v", out, dialed.Load())
	}
}

func TestContactAddressAcceptanceReplayShowsCurrentDestinationWithoutReprobe(t *testing.T) {
	h := newTestHub(t)
	if got := h.loadIdentity(context.Background()); got != "" {
		t.Fatalf("load identity: %s", got)
	}
	key := messageWorkerContact(t, h, 1)
	ctx := context.Background()
	for _, step := range []struct{ id, candidate string }{{"address-A", "first.example:7445"}, {"address-B", "second.example:7445"}} {
		before, found, err := h.store.Contact(ctx, key)
		if err != nil || !found {
			t.Fatalf("missing contact: %v", err)
		}
		_, known, err := h.store.BeginContactAddress(ctx, store.AddressOp{ID: step.id, Contact: key, Candidate: step.candidate, At: time.Now()})
		if err != nil || known {
			t.Fatalf("begin %s: known=%v err=%v", step.id, known, err)
		}
		_, err = h.store.ChangeContactAddress(ctx, store.ContactAddress{OpID: step.id, Contact: key, From: before.Destination,
			FromAt: before.DestinationAt, To: step.candidate, At: time.Now(), StillDials: func(peer.Contact) bool { return true }})
		if err != nil {
			t.Fatal(err)
		}
	}
	var oldCandidateDials atomic.Int32
	h.SetPairing(PairingConfig{Clock: time.Now, Dial: func(_ context.Context, destination string) (net.Conn, error) {
		if destination == "first.example:7445" {
			oldCandidateDials.Add(1)
		}
		return nil, context.DeadlineExceeded
	}})
	runCtx, cancel := context.WithCancel(ctx)
	defer func() { cancel(); <-h.Done() }()
	go h.Run(runCtx)
	out := h.SubmitOperator(runCtx, 0, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpAddress,
		ContactKey: key, ContactText: "first.example:7445", MessageID: "address-A"})
	if out.Type != protocol.TypeOperatorContactResult || out.PairingResult == nil {
		t.Fatalf("accepted operation replay refused: %+v", out)
	}
	foundCurrent := false
	for _, contact := range out.PairingResult.Contacts {
		if contact.IdentityKey == key {
			foundCurrent = true
			if contact.Destination != "second.example:7445" {
				t.Fatalf("replay showed stale candidate: %+v", contact)
			}
		}
	}
	if !foundCurrent || oldCandidateDials.Load() != 0 {
		t.Fatalf("replay missing contact or redialed stale candidate: found=%v dials=%d", foundCurrent, oldCandidateDials.Load())
	}
}

func TestContactAddressAcceptanceSessionKeepsCorrelationUntilFinalAnswer(t *testing.T) {
	h := newTestHub(t)
	if got := h.loadIdentity(context.Background()); got != "" {
		t.Fatalf("load identity: %s", got)
	}
	key := messageWorkerContact(t, h, 1)
	started, release := make(chan struct{}, 1), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	h.SetPairing(PairingConfig{Clock: time.Now, Dial: func(ctx context.Context, destination string) (net.Conn, error) {
		if destination != "session.example:7445" {
			return nil, context.DeadlineExceeded
		}
		started <- struct{}{}
		select {
		case <-release:
			return nil, context.DeadlineExceeded
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-h.Done() }()
	go h.Run(ctx)
	sess := NewOperatorSession(0)
	if out := h.OpenOperatorSession(ctx, protocol.Message{Type: protocol.TypeOperatorSessionOpen, RequestID: "open"}, sess); out.Type != protocol.TypeOperatorSession {
		t.Fatalf("session open: %+v", out)
	}
	address := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpAddress,
		ContactKey: key, ContactText: "session.example:7445", MessageID: "session-move", RequestID: "address-request"}
	if !h.SessionRequest(ctx, sess, address) {
		t.Fatal("address session request not admitted")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("session probe never started")
	}
	if !h.SessionRequest(ctx, sess, protocol.Message{Type: protocol.TypeOperatorPairing,
		PairingOp: protocol.PairingOpList, RequestID: "other-request"}) {
		t.Fatal("other session request not admitted")
	}
	if got := sessionQueueNext(t, sess); got.RequestID != "other-request" || got.Type != protocol.TypeOperatorPairingResult {
		t.Fatalf("address check blocked or preempted other response: %+v", got)
	}
	close(release)
	if got := sessionQueueNext(t, sess); got.RequestID != "address-request" || got.MessageID != "session-move" || got.Type != protocol.TypeError {
		t.Fatalf("address final answer lost correlation: %+v", got)
	}
	// An async request has precisely one final answer, never a premature ACK.
	short, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	if extra, ok := sess.Next(short); ok {
		t.Fatalf("address request answered twice: %+v", extra)
	}
}

func TestContactAddressAcceptanceShutdownCancelsProbeAndSettlesAudit(t *testing.T) {
	h := newTestHub(t)
	if got := h.loadIdentity(context.Background()); got != "" {
		t.Fatalf("load identity: %s", got)
	}
	key := messageWorkerContact(t, h, 1)
	started := make(chan struct{}, 1)
	h.SetPairing(PairingConfig{Clock: time.Now, Dial: func(ctx context.Context, candidate string) (net.Conn, error) {
		if candidate != "cancel.example:7445" {
			return nil, context.DeadlineExceeded
		}
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx)
	answer := make(chan protocol.Message, 1)
	go func() {
		answer <- h.SubmitOperator(ctx, 0, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpAddress,
			ContactKey: key, ContactText: "cancel.example:7445", MessageID: "cancel-move"})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("probe never started")
	}
	cancel()
	select {
	case <-h.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not cancel probe promptly")
	}
	select {
	case <-answer:
	case <-time.After(time.Second):
		t.Fatal("shutdown stranded waiting operator")
	}
	contact, found, err := h.store.Contact(context.Background(), key)
	if err != nil || !found || contact.Destination != "peer-1:443" {
		t.Fatalf("canceled probe moved contact: %+v found=%v err=%v", contact, found, err)
	}
	outcome, known, err := h.store.BeginContactAddress(context.Background(), store.AddressOp{ID: "cancel-move", Contact: key,
		Candidate: "cancel.example:7445", At: time.Now()})
	if err != nil || !known || outcome.State != store.AddressRefused {
		t.Fatalf("shutdown left pending address audit: outcome=%+v known=%v err=%v", outcome, known, err)
	}
	raw, err := sql.Open("sqlite", strings.TrimSuffix(h.store.PeerDir(), ".peer"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var rejected, pending int
	if err := raw.QueryRow(`SELECT count(*) FROM operator_audit WHERE action='contact address' AND message_id='cancel-move' AND result='rejected'`).Scan(&rejected); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM operator_audit WHERE action='contact address' AND message_id='cancel-move' AND result='pending'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if rejected != 1 || pending != 0 {
		t.Fatalf("shutdown did not reject original audit: rejected=%d pending=%d", rejected, pending)
	}
}

func TestContactAddressAcceptanceBlockedProbeDoesNotBlockHub(t *testing.T) {
	h := newTestHub(t)
	started := make(chan string, 1)
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	h.SetPairing(PairingConfig{Clock: time.Now, Dial: func(ctx context.Context, address string) (net.Conn, error) {
		if address != "new.example:7445" {
			return nil, context.DeadlineExceeded
		}
		started <- address
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return nil, context.DeadlineExceeded
		}
	}})
	if got := h.loadIdentity(context.Background()); got != "" {
		t.Fatalf("load identity: %s", got)
	}
	key := messageWorkerContact(t, h, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-h.Done() }()
	go h.Run(ctx)
	requestCtx, stop := context.WithCancel(ctx)
	defer stop()
	answer := make(chan protocol.Message, 1)
	go func() {
		answer <- h.SubmitOperator(requestCtx, 0, protocol.Message{Type: protocol.TypeOperatorContact,
			ContactOp: protocol.ContactOpAddress, ContactKey: key, ContactText: "new.example:7445", MessageID: "move-1"})
	}()
	select {
	case got := <-started:
		if got != "new.example:7445" {
			t.Fatalf("probe dialed %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("address check never started")
	}
	// There is no premature success and a competing move is refused promptly.
	select {
	case premature := <-answer:
		t.Fatalf("probe answered before verification: %+v", premature)
	default:
	}
	quick, end := context.WithTimeout(ctx, 2*time.Second)
	defer end()
	competing := make(chan protocol.Message, 1)
	go func() {
		competing <- h.SubmitOperator(quick, 0, protocol.Message{Type: protocol.TypeOperatorContact,
			ContactOp: protocol.ContactOpAddress, ContactKey: key, ContactText: "another.example:7445", MessageID: "move-2"})
	}()
	select {
	case out := <-competing:
		if out.Type != protocol.TypeError {
			t.Fatalf("second move started while probe runs: %+v", out)
		}
	case <-quick.Done():
		t.Fatal("competing address request waited for blocked probe")
	}
	// An unrelated operator request must be handled while the probe is stuck.
	other := make(chan protocol.Message, 1)
	go func() {
		other <- h.SubmitOperator(quick, 0, protocol.Message{Type: protocol.TypeOperatorPairing,
			PairingOp: protocol.PairingOpList, MessageID: "other"})
	}()
	select {
	case got := <-other:
		if got.Type == protocol.TypeError {
			t.Fatalf("unrelated request blocked or failed: %+v", got)
		}
	case <-quick.Done():
		t.Fatal("blocked address probe stalled the hub")
	}
	close(release)
	select {
	case response := <-answer:
		if response.Type != protocol.TypeError {
			t.Fatalf("failed probe reported success: %+v", response)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failed probe did not settle promptly")
	}
	stop()
	contact, found, err := h.store.Contact(context.Background(), key)
	if err != nil || !found || contact.Destination != "peer-1:443" {
		t.Fatalf("failed probe changed contact: %+v, found=%v, err=%v", contact, found, err)
	}
}
