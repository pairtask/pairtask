package messaging_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// These scripts model the peer independently, so two copies of the same
// session implementation cannot hide a phase-order or correlation mistake.
type routingSessionProbe struct {
	messaging.Store
	versions     []byte
	pending      []messaging.AgentRead
	confirmed    []messaging.AgentRead
	received     []messaging.AgentRead
	directory    []string
	readErr      error
	directoryErr error
}

func (s *routingSessionProbe) OfferFor(_ context.Context, _ string, _ time.Time, _ messaging.Limit, byteVersion byte) ([]messaging.Row, error) {
	s.versions = append(s.versions, byteVersion)
	return nil, nil
}
func (s *routingSessionProbe) PendingAgentReads(context.Context, string, int) ([]messaging.AgentRead, error) {
	return s.pending, nil
}
func (s *routingSessionProbe) AgentReadConfirmed(_ context.Context, _ string, r messaging.AgentRead) error {
	s.confirmed = append(s.confirmed, r)
	return nil
}
func (s *routingSessionProbe) PeerAgentRead(_ context.Context, _ string, r messaging.AgentRead, _ time.Time) error {
	s.received = append(s.received, r)
	return s.readErr
}
func (s *routingSessionProbe) SharedAgents(context.Context, string) ([]string, error) {
	return []string{"local\nagent"}, nil
}
func (s *routingSessionProbe) SetPeerDirectory(_ context.Context, _ string, agents []string, _ time.Time) error {
	s.directory = agents
	return s.directoryErr
}

func routingSessionHello(t *testing.T, version byte) []byte {
	t.Helper()
	b, err := messaging.EncodeHello(messaging.Hello{Min: 1, Max: version})
	return sessionFrame(t, b, err)
}
func routingSessionEnd(t *testing.T) []byte {
	t.Helper()
	b, err := messaging.EncodeEnd()
	return sessionFrame(t, b, err)
}
func routingSessionPrefix(t *testing.T) [][]byte {
	t.Helper()
	// Dialer receives the peer's message End, then its read End.
	return [][]byte{routingSessionHello(t, 3), routingSessionEnd(t), routingSessionEnd(t)}
}

func TestRoutingSessionAcceptanceDowngradeSkipsRoutingPhases(t *testing.T) {
	for _, version := range []byte{1, 2} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			db, _ := sessionFixture(t)
			probe := &routingSessionProbe{Store: db.PeerMessages()}
			f := &scriptFrames{input: [][]byte{routingSessionHello(t, version), routingSessionEnd(t), routingSessionEnd(t)}}
			_, got, err := sessionSide(f, probe).OpenDial(context.Background())
			sessionOK(t, err)
			sessionEqual(t, got, version)
			if !reflect.DeepEqual(probe.versions, []byte{version}) {
				t.Fatalf("OfferFor versions: %v", probe.versions)
			}
			sessionEqual(t, len(f.sent), 3) // Hello, message End, read End; no directory or agent receipts.
		})
	}
}

func TestRoutingSessionAcceptanceReceiptBindsBothIDAndAgent(t *testing.T) {
	wanted := messaging.AgentRead{ID: "same-message", Agent: "agent-a"}
	for _, wrong := range []messaging.AgentRead{{ID: wanted.ID, Agent: "agent-b"}, {ID: "other-message", Agent: wanted.Agent}} {
		t.Run(wrong.ID+wrong.Agent, func(t *testing.T) {
			db, _ := sessionFixture(t)
			probe := &routingSessionProbe{Store: db.PeerMessages(), pending: []messaging.AgentRead{wanted}}
			b, err := messaging.EncodeAgentReadStored(wrong)
			f := &scriptFrames{input: append(routingSessionPrefix(t), sessionFrame(t, b, err))}
			_, _, err = sessionSide(f, probe).OpenDial(context.Background())
			sessionErrorIs(t, err, messaging.ErrProtocol)
			sessionEqual(t, len(probe.confirmed), 0)
		})
	}
}

func TestRoutingSessionAcceptanceRejectsForeignRecipientWithoutAcknowledging(t *testing.T) {
	db, _ := sessionFixture(t)
	probe := &routingSessionProbe{Store: db.PeerMessages(), readErr: messaging.ErrNotRecipient}
	b, err := messaging.EncodeAgentRead(messaging.AgentRead{ID: "message", Agent: "outsider"})
	f := &scriptFrames{input: append(routingSessionPrefix(t), sessionFrame(t, b, err))}
	_, _, err = sessionSide(f, probe).OpenDial(context.Background())
	sessionErrorIs(t, err, messaging.ErrProtocol)
	var storeErr messaging.StoreError
	if errors.As(err, &storeErr) {
		t.Fatalf("peer violation classified as store failure: %v", err)
	}
	for _, frame := range f.sent {
		kind, _ := messaging.Kind(frame)
		if kind == messaging.FrameAgentReadStored {
			t.Fatal("acknowledged foreign recipient")
		}
	}
}

func TestRoutingSessionAcceptanceDirectoryFailureDoesNotAcknowledge(t *testing.T) {
	db, _ := sessionFixture(t)
	probe := &routingSessionProbe{Store: db.PeerMessages(), directoryErr: io.ErrClosedPipe}
	directory, err := messaging.EncodeDirectory([]string{"remote:verbatim/@\nagent"})
	input := append(routingSessionPrefix(t), routingSessionEnd(t), routingSessionEnd(t), sessionFrame(t, directory, err))
	f := &scriptFrames{input: input}
	_, _, err = sessionSide(f, probe).OpenDial(context.Background())
	sessionErrorIs(t, err, io.ErrClosedPipe)
	var storeErr messaging.StoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("store failure lost classification: %v", err)
	}
	if !reflect.DeepEqual(probe.directory, []string{"remote:verbatim/@\nagent"}) {
		t.Fatalf("directory mutated: %q", probe.directory)
	}
	last, _ := messaging.Kind(f.sent[len(f.sent)-1])
	sessionEqual(t, last, messaging.FrameDirectory) // No End acknowledging failed persistence.
}
