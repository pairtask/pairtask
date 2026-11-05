package messaging_test

import (
	"context"
	"encoding/binary"
	"errors"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"io"
	"testing"
)

// Encode the peer's transcript independently of the production codec.
func consentRawFrame(kind byte, fields ...string) []byte {
	frame := []byte{1, kind}
	for _, s := range fields {
		frame = binary.BigEndian.AppendUint16(frame, uint16(len(s)))
		frame = append(frame, []byte(s)...)
	}
	return frame
}

func TestConsentWireAcceptanceNegotiatesV8(t *testing.T) {
	db, _ := sessionFixture(t)
	hello, err := messaging.EncodeHello(messaging.Hello{Min: 8, Max: 8})
	sessionOK(t, err)
	frames := &scriptFrames{input: [][]byte{hello}}
	_, version, err := sessionSide(frames, db.PeerMessages()).OpenServe(context.Background())
	if version != 8 || !errors.Is(err, io.EOF) {
		t.Fatalf("consent-capable session failed v8 negotiation: version=%d err=%v", version, err)
	}
}

func TestConsentWireAcceptanceHeldStopsRequesterOffering(t *testing.T) {
	db, _ := sessionFixture(t)
	_, err := db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: sessionContact, ID: "held-request", Text: "immutable content", To: []string{"recipient"}, Now: sessionAt})
	sessionOK(t, err)
	frames := &scriptFrames{input: [][]byte{routingSessionHello(t, 8), consentRawFrame(23, "held-request", "receiver-hold-293")}}
	_, version, err := sessionSide(frames, db.PeerMessages()).OpenDial(context.Background())
	if version != 8 || !errors.Is(err, io.EOF) {
		t.Fatalf("valid Held reply was not consumed durably: version=%d err=%v", version, err)
	}
	rows := sessionRows(t, db.PeerMessages(), 1)
	sessionEqual(t, rows[0].State, messaging.StateDelivered)
	offers, err := db.PeerMessages().Offer(context.Background(), sessionContact, sessionAt, messaging.SessionLimit)
	sessionOK(t, err)
	sessionEqual(t, len(offers), 0)
}

func TestConsentWireAcceptanceWrongHeldIDCannotSettleAnotherRequest(t *testing.T) {
	db, _ := sessionFixture(t)
	_, err := db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: sessionContact, ID: "wanted", Text: "immutable content", Now: sessionAt})
	sessionOK(t, err)
	frames := &scriptFrames{input: [][]byte{routingSessionHello(t, 8), consentRawFrame(23, "other-request", "receiver-hold-293")}}
	_, _, err = sessionSide(frames, db.PeerMessages()).OpenDial(context.Background())
	sessionErrorIs(t, err, messaging.ErrProtocol)
	sessionEqual(t, sessionRows(t, db.PeerMessages(), 1)[0].State, messaging.StateQueued)
}

func TestConsentWireAcceptanceV7NeverReceivesNewHeldFrame(t *testing.T) {
	db, _ := sessionFixture(t)
	_, err := db.ClaimPendingFor(context.Background(), protocol.Recipient{AgentID: "recipient", SessionID: "offline"}, 24)
	sessionOK(t, err)
	sessionOK(t, db.SetConsentOptIn(context.Background(), sessionContact, true, 0, sessionAt))
	sessionOK(t, db.ResetConsentScope(context.Background(), sessionContact, "recipient", protocol.ConsentMessage, 0, sessionAt))
	m := messaging.Message{ID: "legacy-inbound", N: 1, From: "sender", To: []string{"recipient"}, CreatedAt: sessionAt, Text: "requires consent"}
	routed, err := messaging.EncodeRouted(m)
	sessionOK(t, err)
	frames := &scriptFrames{input: [][]byte{routingSessionHello(t, 7), routed}}
	_, _, err = sessionSide(frames, db.PeerMessages()).OpenServe(context.Background())
	sessionErrorIs(t, err, io.EOF)
	refused := false
	for _, frame := range frames.sent {
		kind, err := messaging.Kind(frame)
		sessionOK(t, err)
		if kind >= 23 && kind <= 25 {
			t.Fatalf("v7 received consent frame %d", kind)
		}
		if kind == messaging.FrameRefused {
			r, err := messaging.DecodeRefused(frame)
			sessionOK(t, err)
			sessionEqual(t, r.ID, m.ID)
			sessionEqual(t, r.Reason, messaging.ReasonNotShared)
			refused = true
		}
	}
	if !refused {
		t.Fatal("v7 was not explicitly refused not_shared")
	}
}

func TestConsentWireAcceptanceV7RejectsUnexpectedHeldAnswer(t *testing.T) {
	db, _ := sessionFixture(t)
	_, err := db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: sessionContact, ID: "legacy-outgoing", Text: "body", Now: sessionAt})
	sessionOK(t, err)
	frames := &scriptFrames{input: [][]byte{routingSessionHello(t, 7), consentRawFrame(23, "legacy-outgoing", "hold")}}
	_, version, err := sessionSide(frames, db.PeerMessages()).OpenDial(context.Background())
	sessionEqual(t, version, byte(7))
	sessionErrorIs(t, err, messaging.ErrProtocol)
	sessionEqual(t, sessionRows(t, db.PeerMessages(), 1)[0].State, messaging.StateQueued)
}
