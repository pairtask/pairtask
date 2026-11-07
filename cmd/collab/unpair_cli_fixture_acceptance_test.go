package main

import (
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func unpairCLI(t *testing.T, supported bool) (string, <-chan map[string]any) {
	t.Helper()
	key := "sha256:" + strings.Repeat("a", 64)
	return pairingCLISocket(t, func(req map[string]any) map[string]any {
		snapshot := map[string]any{"unpair_available": supported, "contacts": []any{map[string]any{"identity_key": key, "name": "Peer", "confirmed": true, "pairing_epoch": 7}}}
		if req["type"] == protocol.TypeOperatorPairing {
			return map[string]any{"type": protocol.TypeOperatorPairingResult, "pairing_result": snapshot}
		}
		return map[string]any{"type": protocol.TypeOperatorContactResult, "contact_op": "unpair", "contact_key": key, "message_id": req["message_id"], "contact_unpaired": map[string]any{"changed": true, "generation": 8, "epoch": 7, "unpaired_at": "2026-11-07T06:00:00Z"}, "pairing_result": snapshot}
	})
}
func unpairCLINoMutation(t *testing.T, requests <-chan map[string]any) {
	t.Helper()
	for len(requests) > 0 {
		req := <-requests
		if req["type"] == protocol.TypeOperatorContact {
			t.Fatalf("unexpected mutation: %v", req)
		}
	}
}
