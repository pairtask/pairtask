package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

// A one-shot command uses the operator socket directly, so it can move a
// contact while the operator's interactive chat owns the inbox.
func TestContactAddressCommandAcceptanceResolvesNameAndReportsMove(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	destination := "10.10.53.202:7445"
	path, requests := pairingCLISocket(t, func(req map[string]any) map[string]any {
		contact := map[string]any{"identity_key": key, "name": "Work Mac", "local_name": "Office", "confirmed": true, "dials": true}
		if req["type"] == protocol.TypeOperatorContact && req["contact_op"] == protocol.ContactOpAddress {
			contact["destination"] = destination
			return map[string]any{"type": protocol.TypeOperatorContactResult, "pairing_result": map[string]any{"contacts": []any{contact}}}
		}
		return map[string]any{"type": protocol.TypeOperatorPairingResult, "pairing_result": map[string]any{"contacts": []any{contact}}}
	})
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"operator", "contact", "address", "Office", destination, "--socket", path}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("one-shot address move failed: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), destination) {
		t.Fatalf("the new address was not reported: %q", stdout.String())
	}
	assertCLIAddressListRequest(t, roomCLIRequest(t, requests))
	assertCLIAddressMoveRequest(t, roomCLIRequest(t, requests), key, destination)
	assertNoCLIAddressExtraRequest(t, requests)
}

func assertCLIAddressListRequest(t *testing.T, read map[string]any) {
	t.Helper()
	if read["type"] != protocol.TypeOperatorPairing {
		t.Fatalf("contact name was not resolved from the pairing list: %v", read)
	}
	if read["pairing_op"] != protocol.PairingOpList {
		t.Fatalf("contact name was not resolved from the pairing list: %v", read)
	}
}

func assertCLIAddressMoveRequest(t *testing.T, write map[string]any, key, destination string) {
	t.Helper()
	if write["type"] != protocol.TypeOperatorContact {
		t.Fatalf("address move was not sent as one checked operator request: %v", write)
	}
	if write["contact_op"] != protocol.ContactOpAddress {
		t.Fatalf("address move was not sent as one checked operator request: %v", write)
	}
	if write["contact_key"] != key {
		t.Fatalf("address move used the wrong contact or destination: %v", write)
	}
	if write["contact_text"] != destination {
		t.Fatalf("address move used the wrong contact or destination: %v", write)
	}
}

func assertNoCLIAddressExtraRequest(t *testing.T, requests <-chan map[string]any) {
	t.Helper()
	select {
	case extra := <-requests:
		t.Fatalf("one-shot command opened another session or sent another request: %v", extra)
	default:
	}
}

func TestContactAddressCommandAcceptanceBrokerRefusalIsNotReportedAsMove(t *testing.T) {
	key := "sha256:" + strings.Repeat("b", 64)
	old := "10.10.53.101:7445"
	path, requests := pairingCLISocket(t, func(req map[string]any) map[string]any {
		if req["type"] == protocol.TypeOperatorContact {
			return map[string]any{"type": protocol.TypeError, "code": "wrong_identity", "detail": "a different workspace answered at the new address"}
		}
		return map[string]any{"type": protocol.TypeOperatorPairingResult, "pairing_result": map[string]any{"contacts": []any{
			map[string]any{"identity_key": key, "name": "Office", "confirmed": true, "dials": true, "destination": old},
		}}}
	})
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"operator", "contact", "address", "Office", "10.10.53.202:7445", "--socket", path}, &stdout, &stderr)
	if exit == 0 || !strings.Contains(stderr.String(), "different workspace") {
		t.Fatalf("wrong identity was presented as a successful move: exit=%d out=%q err=%q", exit, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "10.10.53.202:7445") {
		t.Fatalf("refused destination was shown as current: %q", stdout.String())
	}
	_ = roomCLIRequest(t, requests) // read the existing contact first
	write := roomCLIRequest(t, requests)
	if write["contact_op"] != protocol.ContactOpAddress || write["contact_key"] != key {
		t.Fatalf("refusal did not come from the broker's checked address operation: %v", write)
	}
}

func TestContactAddressCommandAcceptanceJSONShowsUpdatedContact(t *testing.T) {
	key := "sha256:" + strings.Repeat("c", 64)
	destination := "office.example:7445"
	path, _ := pairingCLISocket(t, func(req map[string]any) map[string]any {
		contact := map[string]any{"identity_key": key, "name": "Office", "confirmed": true, "dials": true, "destination": destination}
		if req["type"] == protocol.TypeOperatorContact {
			return map[string]any{"type": protocol.TypeOperatorContactResult, "pairing_result": map[string]any{"contacts": []any{contact}}}
		}
		return map[string]any{"type": protocol.TypeOperatorPairingResult, "pairing_result": map[string]any{"contacts": []any{contact}}}
	})
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"operator", "contact", "address", "Office", destination, "--socket", path, "--json"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("JSON address command failed: exit=%d out=%q err=%q", exit, stdout.String(), stderr.String())
	}
	if !json.Valid(stdout.Bytes()) {
		t.Fatalf("address result is not JSON: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), destination) {
		t.Fatalf("JSON address result: exit=%d out=%q err=%q", exit, stdout.String(), stderr.String())
	}
}
