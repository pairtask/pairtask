//go:build linux

package server

import (
	"context"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"net"
	"strings"
	"testing"
	"time"
)

func TestContactEventsAcceptancePartialInboundOnlyNotifiesOperator(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	id := messageIdentity(t)
	messageConfirm(f, id, 1)
	agent := f.peer("observer", protocol.Version)
	owner := sessionOpen(f.operatorFixture)
	owner.snapshot()
	ep, err := f.h.PairingEndpoint()
	operatorNoError(t, err)
	conn, err := messageConnect(t, messageRoute{relayToPairing(t, f), ep.Identity.Fingerprint()}, id)
	operatorNoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	frames := conn.MessageFrames(ctx)
	wire, err := messaging.EncodeMessage(messaging.Message{ID: "partial", N: 1, CreatedAt: time.Now(), Text: "PRIVATE_CONTACT_BODY"})
	operatorNoError(t, err)
	operatorNoError(t, frames.Send(wire))
	ack, err := frames.Recv()
	operatorNoError(t, err)
	stored, err := messaging.DecodeStored(ack)
	operatorNoError(t, err)
	operatorEqual(t, "durable before failure", stored.ID, "partial")
	// Close before End: the session fails after committing the inbound row.
	conn.Close()
	// The version alert and the committed message can be announced in either
	// order, but the operator must receive both without the private body.
	assertPartialInboundOperatorEvents(t, owner, id.Fingerprint())
	rows, _, err := f.st.PeerMessages().Page(context.Background(), id.Fingerprint(), 0, 64)
	operatorNoError(t, err)
	operatorEqual(t, "one retained message", len(rows), 1)
	operatorNoError(t, agent.SetReadDeadline(time.Now().Add(30*time.Millisecond)))
	_, err = agent.r.ReadByte()
	if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("agent received contact traffic: %v", err)
	}
}

func assertPartialInboundOperatorEvents(t *testing.T, owner *sessionProbe, contactKey string) {
	t.Helper()
	seen := make(map[string]bool)
	// Two distinct events from the two allowed kinds require both kinds.
	for range 2 {
		frame := owner.frame()
		event := sessionDecode[protocol.SessionEvent](t, frame, "event")
		if strings.Contains(sessionJSON(frame), "PRIVATE_CONTACT_BODY") {
			t.Fatalf("%s event exposed text", event.Kind)
		}
		if event.Kind != protocol.EventPairingChanged && event.Kind != protocol.EventContactMessagesChanged {
			t.Fatalf("unexpected operator event: %s", event.Kind)
		}
		if seen[event.Kind] {
			t.Fatalf("duplicate operator event: %s", event.Kind)
		}
		seen[event.Kind] = true
		if event.Kind == protocol.EventContactMessagesChanged {
			operatorEqual(t, "attributed contact", event.ContactKey, contactKey)
		}
	}
}
