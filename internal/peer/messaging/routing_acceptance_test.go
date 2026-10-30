package messaging_test

import (
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func routedFixture() messaging.Message {
	return messaging.Message{ID: "routed", N: 7, CreatedAt: sessionAt, Text: "hello remote agents",
		From: "sender@home", To: []string{"agent\nsecond-line", "remote:existing/id"},
		ReplyTo: messaging.ReplyRef{ID: "parent", Author: messaging.ReplyByReceiver}}
}

func TestRoutingWireAcceptanceLegacyHashIsUnchanged(t *testing.T) {
	m := messaging.Message{ID: "legacy", N: 7, CreatedAt: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC), Text: "unchanged wire identity"}
	hash := m.Hash()
	if got := hex.EncodeToString(hash[:]); got != "bbb373ddd28b94f51a02faaa6177e4f38af05822dbe22981426372d9bac8e074" {
		t.Fatalf("legacy dedup identity changed: %s", got)
	}
	if m.Routed() {
		t.Fatal("legacy operator message treated as routed")
	}
}

func TestRoutingWireAcceptanceRoundTripKeepsIDsAndReplyDirection(t *testing.T) {
	m := routedFixture()
	wire, err := messaging.EncodeRouted(m)
	sessionOK(t, err)
	decoded, err := messaging.DecodeRouted(wire)
	sessionOK(t, err)
	if !reflect.DeepEqual(decoded, m) {
		t.Fatalf("routed fields changed: got %+v want %+v", decoded, m)
	}
	if !decoded.Routed() {
		t.Fatal("routed message lost classification")
	}
	kind, err := messaging.Kind(wire)
	sessionOK(t, err)
	sessionEqual(t, kind, messaging.FrameRouted)
	for n := 0; n < len(wire); n++ {
		if _, err := messaging.DecodeRouted(wire[:n]); !errors.Is(err, messaging.ErrProtocol) {
			t.Fatalf("truncated routed frame (%d bytes) accepted: %v", n, err)
		}
	}
	if _, err := messaging.DecodeRouted(append(wire, 0)); !errors.Is(err, messaging.ErrProtocol) {
		t.Fatalf("trailing data accepted: %v", err)
	}
}

func TestRoutingWireAcceptanceHashBindsSenderRecipientsAndReply(t *testing.T) {
	base := routedFixture()
	original := base.Hash()
	for _, change := range []func(*messaging.Message){
		func(m *messaging.Message) { m.From = "other" },
		func(m *messaging.Message) { m.To = []string{"other"} },
		func(m *messaging.Message) { m.ReplyTo.ID = "other" },
		func(m *messaging.Message) { m.ReplyTo.Author = messaging.ReplyBySender },
	} {
		altered := base
		change(&altered)
		if altered.Hash() == original {
			t.Fatalf("routing mutation left identity unchanged: %+v", altered)
		}
	}
}

func TestRoutingWireAcceptanceBoundedRecipientsAndValidReply(t *testing.T) {
	for _, change := range []func(*messaging.Message){
		func(m *messaging.Message) {
			m.To = make([]string, 65)
			for i := range m.To {
				m.To[i] = strings.Repeat("a", i+1)
			}
		},
		func(m *messaging.Message) { m.To = []string{""} },
		func(m *messaging.Message) { m.ReplyTo.Author = 99 },
		func(m *messaging.Message) { m.ReplyTo.Author = messaging.ReplyAuthorNone },
		func(m *messaging.Message) { m.ReplyTo.ID = "" },
	} {
		m := routedFixture()
		change(&m)
		wire, err := messaging.EncodeRouted(m)
		if err == nil {
			_, err = messaging.DecodeRouted(wire)
		}
		if err == nil {
			t.Fatalf("invalid routed shape accepted: %+v", m)
		}
	}
}

func TestRoutingWireAcceptanceAgentReceiptIncludesRecipient(t *testing.T) {
	ack := messaging.AgentRead{ID: "same-message", Agent: "agent\nwith@delimiters"}
	wire, err := messaging.EncodeAgentRead(ack)
	sessionOK(t, err)
	got, err := messaging.DecodeAgentRead(wire)
	sessionOK(t, err)
	sessionEqual(t, got, ack)
	stored, err := messaging.EncodeAgentReadStored(ack)
	sessionOK(t, err)
	confirmed, err := messaging.DecodeAgentReadStored(stored)
	sessionOK(t, err)
	sessionEqual(t, confirmed, ack)
	if _, err := messaging.DecodeAgentReadStored(wire); !errors.Is(err, messaging.ErrProtocol) {
		t.Fatalf("agent receipt mistaken for its acknowledgment: %v", err)
	}
}
