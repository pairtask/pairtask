package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func repairCLI(t *testing.T, supported bool) (string, <-chan map[string]any) {
	t.Helper()
	return pairingCLISocket(t, func(req map[string]any) map[string]any {
		result := redeemCLIReply("", "", true)
		snapshot := result["pairing_result"].(map[string]any)
		snapshot["repair_available"] = supported
		if req["pairing_op"] == "open" {
			snapshot["invitation"] = map[string]any{"id": "repair-invite", "code": "739-281"}
		}
		if req["pairing_op"] == "redeem" {
			result["pairing_join"] = map[string]any{"operation_id": req["message_id"], "state": "awaiting_acceptance", "destination": "host:443"}
		}
		return result
	})
}
func TestRepairCLIAcceptanceOpenDeclaresFullKeyAfterCapabilityProbe(t *testing.T) {
	path, requests := repairCLI(t, true)
	key := "sha256:" + strings.Repeat("a", 64)
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "pairing", "open", "--repair", key, "--socket", path}, &out, &stderr)
	assertStatusEqual(t, stderr.String(), code, 0)
	probe := roomCLIRequest(t, requests)
	assertStatusEqual(t, "repair capability first", roomCLIString(probe, "pairing_op"), "status")
	write := roomCLIRequest(t, requests)
	assertStatusEqual(t, "open repair", roomCLIString(write, "pairing_op"), "open")
	assertStatusEqual(t, "declared repair key", roomCLIString(write, "pairing_repair"), key)
}
func TestRepairCLIAcceptanceRedeemDeclaresKeyAndReadsSecretAfterProbe(t *testing.T) {
	redeemCLIInput(t, "739-281\n")
	path, requests := repairCLI(t, true)
	key := "sha256:" + strings.Repeat("a", 64)
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "pairing", "redeem", "host:443", "--repair", key, "--socket", path}, &out, &stderr)
	assertStatusEqual(t, stderr.String(), code, 0)
	probe := roomCLIRequest(t, requests)
	assertStatusEqual(t, "repair capability first", roomCLIString(probe, "pairing_op"), "status")
	write := roomCLIRequest(t, requests)
	assertStatusEqual(t, "redeem repair", roomCLIString(write, "pairing_op"), "redeem")
	assertStatusEqual(t, "declared repair key", roomCLIString(write, "pairing_repair"), key)
	assertStatusEqual(t, "secret only in redeem request", strings.ReplaceAll(roomCLIString(write, "pairing_code"), "-", ""), "739281")
}
func TestRepairCLIAcceptanceOldBrokerNeverGetsMutationOrSecret(t *testing.T) {
	redeemCLIInput(t, "739-281\n")
	path, requests := repairCLI(t, false)
	key := "sha256:" + strings.Repeat("a", 64)
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "pairing", "redeem", "host:443", "--repair", key, "--socket", path}, &out, &stderr)
	if code == 0 {
		t.Fatal("old broker accepted repair")
	}
	if !strings.Contains(strings.ToLower(stderr.String()), "upgrade") {
		t.Fatalf("missing repair upgrade guidance: %s", stderr.String())
	}
	for len(requests) > 0 {
		req := <-requests
		if req["pairing_op"] != "status" {
			t.Fatalf("old broker got mutation: %v", req)
		}
	}
	position, err := os.Stdin.Seek(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertStatusEqual(t, "secret not read", position, int64(0))
}
