package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func pauseCLI(t *testing.T, supported bool) (string, <-chan map[string]any) {
	t.Helper()
	key := "sha256:" + strings.Repeat("a", 64)
	return pairingCLISocket(t, func(req map[string]any) map[string]any {
		if req["type"] == protocol.TypeOperatorPairing {
			return map[string]any{"type": protocol.TypeOperatorPairingResult, "pairing_result": map[string]any{"pause_available": supported, "contacts": []any{map[string]any{"identity_key": key, "name": "Peer", "local_name": "Local peer", "confirmed": true, "paused_at": "2026-11-01T12:00:00Z"}}}}
		}
		return map[string]any{"type": protocol.TypeOperatorContactResult, "contact_op": req["contact_op"], "contact_key": req["contact_key"], "message_id": req["message_id"]}
	})
}

func TestContactPauseCLIAcceptanceResolvesAndSendsStableOperation(t *testing.T) {
	for _, op := range []string{"pause", "resume"} {
		t.Run(op, func(t *testing.T) {
			path, requests := pauseCLI(t, true)
			var out, stderr bytes.Buffer
			code := run(context.Background(), []string{"operator", "peer", op, "Local peer", "--message-id", "stable-pause", "--socket", path, "--json"}, &out, &stderr)
			assertStatusEqual(t, stderr.String(), code, 0)
			read := roomCLIRequest(t, requests)
			assertStatusEqual(t, "lookup first", roomCLIString(read, "type"), protocol.TypeOperatorPairing)
			write := roomCLIRequest(t, requests)
			assertStatusEqual(t, "contact operation", roomCLIString(write, "contact_op"), op)
			assertStatusEqual(t, "canonical contact", roomCLIString(write, "contact_key"), "sha256:"+strings.Repeat("a", 64))
			assertStatusEqual(t, "stable operation", roomCLIString(write, "message_id"), "stable-pause")
		})
	}
}

func TestContactPauseCLIAcceptanceOldBrokerReceivesNoMutation(t *testing.T) {
	for _, op := range []string{"pause", "resume"} {
		path, requests := pauseCLI(t, false)
		var out, stderr bytes.Buffer
		code := run(context.Background(), []string{"operator", "peer", op, "Local peer", "--socket", path}, &out, &stderr)
		if code == 0 {
			t.Fatal("old broker appeared to support pause")
		}
		if !strings.Contains(stderr.String(), "upgrade") {
			t.Fatalf("missing capability guidance: %s", stderr.String())
		}
		for len(requests) > 0 {
			if req := <-requests; req["type"] == protocol.TypeOperatorContact {
				t.Fatal("old broker received pause mutation")
			}
		}
	}
}

func TestContactPauseCLIAcceptanceInvalidArgumentsNeverContactBroker(t *testing.T) {
	for _, args := range [][]string{{"pause"}, {"resume"}, {"pause", "Peer", "extra"}, {"resume", "Peer", "extra"}} {
		var out, stderr bytes.Buffer
		code := run(context.Background(), append([]string{"operator", "peer"}, args...), &out, &stderr)
		assertStatusEqual(t, "invalid arguments", code, 2)
	}
}
