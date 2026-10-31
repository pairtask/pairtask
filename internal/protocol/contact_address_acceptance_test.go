package protocol_test

import (
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func addressRequest(destination string) protocol.Message {
	return protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpAddress,
		ContactKey: contactTestKey, ContactText: destination, MessageID: "move-1"}
}

func TestContactAddressAcceptanceValidDestinationAndExactFields(t *testing.T) {
	for _, destination := range []string{"new.example:7445", "192.0.2.5:443", "[2001:db8::1]:7445"} {
		if err := protocol.ValidateContactRequest(addressRequest(destination)); err != nil {
			t.Errorf("valid %q rejected: %v", destination, err)
		}
	}
}

func TestContactAddressAcceptanceRejectsInvalidDestinationAndForeignFields(t *testing.T) {
	for _, destination := range []string{"", "new.example", ":443", "new.example:0", "new.example:65536", "new.example:-1", "new.example:http", "2001:db8::1:443", "new.example:443\nforged", strings.Repeat("a", 256) + ":443"} {
		if err := protocol.ValidateContactRequest(addressRequest(destination)); err == nil {
			t.Errorf("invalid destination %q accepted", destination)
		}
	}
	for _, change := range []struct {
		name  string
		apply func(*protocol.Message)
	}{
		{"missing operation id", func(m *protocol.Message) { m.MessageID = "" }},
		{"invalid contact key", func(m *protocol.Message) { m.ContactKey = "sha256:abc" }},
		{"read ids", func(m *protocol.Message) { m.ContactRead = []string{"shown"} }},
		{"pagination", func(m *protocol.Message) { m.ContactBefore = 1 }},
		{"agents", func(m *protocol.Message) { m.ContactAgents = []string{"worker"} }},
		{"review action", func(m *protocol.Message) { m.ReviewOp = "request" }},
		{"pairing action", func(m *protocol.Message) { m.PairingOp = "list" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			msg := addressRequest("new.example:7445")
			change.apply(&msg)
			if err := protocol.ValidateContactRequest(msg); err == nil {
				t.Fatal("invalid address request accepted")
			}
		})
	}
}
