package protocol_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestContactPauseProtocolAcceptanceValidOperations(t *testing.T) {
	for _, op := range []string{"pause", "resume"} {
		m := contactRequest(op)
		m.MessageID = "stable-pause-operation"
		if err := protocol.ValidateContactRequest(m); err != nil {
			t.Fatalf("valid %s rejected: %v", op, err)
		}
	}
}

func TestContactPauseProtocolAcceptanceRejectsForeignFields(t *testing.T) {
	changes := []struct {
		name  string
		apply func(*protocol.Message)
	}{
		{"missing-id", func(m *protocol.Message) { m.MessageID = "" }},
		{"text", func(m *protocol.Message) { m.ContactText = "hidden send" }},
		{"read", func(m *protocol.Message) { m.ContactRead = []string{"unseen"} }},
		{"agents", func(m *protocol.Message) { m.ContactAgents = []string{"worker"} }},
		{"cursor", func(m *protocol.Message) { m.ContactBefore = 1 }},
		{"bad-key", func(m *protocol.Message) { m.ContactKey = "sha256:abc" }},
	}
	for _, op := range []string{"pause", "resume"} {
		for _, tc := range changes {
			t.Run(op+"-"+tc.name, func(t *testing.T) {
				m := contactRequest(op)
				m.MessageID = "pause-operation"
				tc.apply(&m)
				if protocol.ValidateContactRequest(m) == nil {
					t.Fatal("invalid pause/resume accepted")
				}
			})
		}
	}
}

func TestContactPauseProtocolAcceptanceCapabilityAndSnapshot(t *testing.T) {
	if !slices.Contains(protocol.SessionCapabilities, "contact_pause") {
		t.Fatal("operator session lacks contact_pause capability")
	}
	var c protocol.PairingContactView
	if err := json.Unmarshal([]byte(`{"identity_key":"peer","paused_at":"2026-11-01T12:00:00Z"}`), &c); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["paused_at"]) != `"2026-11-01T12:00:00Z"` {
		t.Fatalf("paused timestamp lost: %s", encoded)
	}
}

func TestContactPauseProtocolAcceptanceActiveContactOmitsPausedAt(t *testing.T) {
	encoded, err := json.Marshal(protocol.PairingContactView{IdentityKey: "peer"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, held := fields["paused_at"]; held {
		t.Fatal("active contact exposed a paused timestamp")
	}
}
