package protocol_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func unpairProtocolMessage(t *testing.T, changes map[string]any) protocol.Message {
	t.Helper()
	key := "sha256:" + strings.Repeat("a", 64)
	fields := map[string]any{"type": "operator_contact", "contact_op": "unpair", "contact_key": key, "contact_confirm": key, "contact_epoch": 3, "message_id": "unpair-operation"}
	for k, v := range changes {
		fields[k] = v
	}
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var m protocol.Message
	if err = json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
func TestUnpairProtocolAcceptanceRequiresExactConfirmAndPositiveEpoch(t *testing.T) {
	if err := protocol.ValidateContactRequest(unpairProtocolMessage(t, nil)); err != nil {
		t.Fatalf("valid unpair refused: %v", err)
	}
	for _, change := range []map[string]any{{"contact_confirm": ""}, {"contact_confirm": "sha256:aaa"}, {"contact_confirm": "sha256:" + strings.Repeat("b", 64)}, {"contact_epoch": 0}, {"contact_epoch": -1}, {"message_id": ""}, {"contact_text": "hidden send"}, {"contact_read": []string{"hidden"}}, {"contact_agents": []string{"worker"}}} {
		if protocol.ValidateContactRequest(unpairProtocolMessage(t, change)) == nil {
			t.Fatalf("invalid unpair accepted: %v", change)
		}
	}
}
func TestUnpairProtocolAcceptanceConfirmationAndResultSurviveWire(t *testing.T) {
	m := unpairProtocolMessage(t, nil)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields["contact_confirm"]) == 0 {
		t.Fatal("exact confirmation dropped from wire")
	}
	wire := `{"type":"operator_contact_result","contact_unpaired":{"changed":true,"generation":7,"epoch":3,"unpaired_at":"2026-11-07T06:00:00Z"}}`
	if err = json.Unmarshal([]byte(wire), &m); err != nil {
		t.Fatal(err)
	}
	b, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields["contact_unpaired"]) == 0 {
		t.Fatal("durable unpair result dropped from wire")
	}
}
func TestUnpairProtocolAcceptanceAdvertisesBothCapabilitiesWithoutChangingConsent(t *testing.T) {
	caps := protocol.SessionCapabilitiesFor(protocol.Version)
	for _, capability := range []string{"contact_unpair", "pairing_repair"} {
		if !slices.Contains(caps, capability) {
			t.Fatalf("missing capability %s", capability)
		}
	}
	if protocol.ConsentVersion != 24 {
		t.Fatal("unpair must not change consent negotiation version")
	}
}
