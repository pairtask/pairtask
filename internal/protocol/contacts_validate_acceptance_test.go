package protocol_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

const contactTestKey = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func contactRequest(op string) protocol.Message {
	return protocol.Message{Type: "operator_contact", ContactOp: op, ContactKey: contactTestKey}
}

func TestContactValidationAcceptanceValidOperations(t *testing.T) {
	send := contactRequest("send")
	send.MessageID = "send-id"
	send.ContactText = strings.Repeat("é", 8192)
	read := contactRequest("read")
	read.MessageID = "read-id"
	for i := 0; i < 64; i++ {
		read.ContactRead = append(read.ContactRead, fmt.Sprint("displayed-", i))
	}
	list := contactRequest("list")
	list.ContactBefore = 123
	for _, msg := range []protocol.Message{send, read, list, contactRequest("sync")} {
		if err := protocol.ValidateContactRequest(msg); err != nil {
			t.Fatalf("valid %s refused: %v", msg.ContactOp, err)
		}
	}
}

func TestContactValidationAcceptanceSendRejectsInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*protocol.Message)
	}{
		{"missing ID", func(m *protocol.Message) { m.MessageID = "" }},
		{"long ID", func(m *protocol.Message) { m.MessageID = strings.Repeat("x", 129) }},
		{"control ID", func(m *protocol.Message) { m.MessageID = "bad\nid" }},
		{"empty text", func(m *protocol.Message) { m.ContactText = "" }},
		{"oversize text", func(m *protocol.Message) { m.ContactText = strings.Repeat("é", 8193) }},
		{"invalid UTF8", func(m *protocol.Message) { m.ContactText = string([]byte{0xff}) }},
		{"missing contact", func(m *protocol.Message) { m.ContactKey = "" }},
		{"prefix not full key", func(m *protocol.Message) { m.ContactKey = "sha256:aaaa" }},
		{"read IDs", func(m *protocol.Message) { m.ContactRead = []string{"other"} }},
		{"pagination", func(m *protocol.Message) { m.ContactBefore = 1 }},
		{"review action", func(m *protocol.Message) { m.ReviewOp = "request" }},
		{"pairing action", func(m *protocol.Message) { m.PairingOp = "open" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := contactRequest("send")
			msg.MessageID = "message-id"
			msg.ContactText = "hello"
			tc.change(&msg)
			if protocol.ValidateContactRequest(msg) == nil {
				t.Fatal("invalid contact request accepted")
			}
		})
	}
}

func TestContactValidationAcceptanceReadRequiresBoundedDisplayedIDs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*protocol.Message)
	}{
		{"missing operation ID", func(m *protocol.Message) { m.MessageID = "" }},
		{"no IDs", func(m *protocol.Message) { m.ContactRead = nil }},
		{"too many IDs", func(m *protocol.Message) {
			m.ContactRead = make([]string, 65)
			for i := range m.ContactRead {
				m.ContactRead[i] = "valid"
			}
		}},
		{"empty ID", func(m *protocol.Message) { m.ContactRead = []string{""} }},
		{"invalid ID", func(m *protocol.Message) { m.ContactRead = []string{"bad\nid"} }},
		{"text not allowed", func(m *protocol.Message) { m.ContactText = "hidden send" }},
		{"cursor not allowed", func(m *protocol.Message) { m.ContactBefore = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := contactRequest("read")
			msg.MessageID = "read-id"
			msg.ContactRead = []string{"shown"}
			tc.change(&msg)
			if protocol.ValidateContactRequest(msg) == nil {
				t.Fatal("invalid read request accepted")
			}
		})
	}
}

func TestContactValidationAcceptanceListAndSyncRejectMutatingFields(t *testing.T) {
	for _, op := range []string{"list", "sync"} {
		for _, change := range []func(*protocol.Message){
			func(m *protocol.Message) { m.ContactText = "text" },
			func(m *protocol.Message) { m.ContactRead = []string{"id"} },
			func(m *protocol.Message) { m.ContactBefore = -1 },
		} {
			msg := contactRequest(op)
			change(&msg)
			if protocol.ValidateContactRequest(msg) == nil {
				t.Fatalf("unexpected fields accepted by %s", op)
			}
		}
	}
	if protocol.ValidateContactRequest(contactRequest("unknown")) == nil {
		t.Fatal("unknown operation accepted")
	}
}
