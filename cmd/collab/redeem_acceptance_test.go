package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func redeemCLIInput(t *testing.T, code string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(code); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = old; f.Close() })
}
func redeemCLIReply(id, state string, available bool) map[string]any {
	result := pairingCLIResponse("", nil, 0)
	snapshot := result["pairing_result"].(map[string]any)
	snapshot["redeem_available"] = available
	snapshot["joins"] = []any{}
	snapshot["joins_total"] = 0
	if id != "" {
		result["pairing_join"] = map[string]any{"operation_id": id, "state": state, "destination": "host:443", "name": "Bob"}
	}
	return result
}
func TestRedeemCLIAcceptanceReadsStdinAndPollsExactOperation(t *testing.T) {
	redeemCLIInput(t, "739-281\n")
	path, requests := pairingCLISocket(t, func(req map[string]any) map[string]any {
		if req["pairing_op"] == "redeem" {
			return redeemCLIReply(req["message_id"].(string), "intent", true)
		}
		if target, ok := req["pairing_target"].(string); ok && target != "" {
			return redeemCLIReply(target, "awaiting_acceptance", true)
		}
		return redeemCLIReply("", "", true)
	})
	var out, stderr bytes.Buffer
	exit := run(context.Background(), []string{"operator", "pair", "redeem", "host:443", "--name", "Bob", "--socket", path, "--wait", "2s"}, &out, &stderr)
	assertStatusEqual(t, "redeem CLI: "+stderr.String(), exit, 0)
	probe := roomCLIRequest(t, requests)
	assertStatusEqual(t, "availability before secret", roomCLIString(probe, "pairing_op"), "status")
	write := roomCLIRequest(t, requests)
	assertStatusEqual(t, "one mutation", roomCLIString(write, "pairing_op"), "redeem")
	assertStatusEqual(t, "destination", roomCLIString(write, "pairing_destination"), "host:443")
	assertStatusEqual(t, "name", roomCLIString(write, "pairing_name"), "Bob")
	assertStatusEqual(t, "code from stdin", strings.ReplaceAll(roomCLIString(write, "pairing_code"), "-", ""), "739281")
	poll := roomCLIRequest(t, requests)
	assertStatusEqual(t, "exact operation poll", roomCLIString(poll, "pairing_target"), roomCLIString(write, "message_id"))
	assertStatusEqual(t, "state displayed", strings.Contains(out.String(), "awaiting_acceptance"), true)
	assertStatusEqual(t, "code absent from output", strings.Contains(out.String()+stderr.String(), "739-281"), false)
	assertStatusEqual(t, "normalized code absent", strings.Contains(out.String()+stderr.String(), "739281"), false)
}

func TestRedeemCLIAcceptanceOldBrokerRefusedBeforeReadingCode(t *testing.T) {
	redeemCLIInput(t, "739-281\n")
	path, requests := pairingCLISocket(t, func(map[string]any) map[string]any { return pairingCLIResponse("", nil, 0) })
	var out, stderr bytes.Buffer
	exit := run(context.Background(), []string{"operator", "pair", "redeem", "host:443", "--socket", path}, &out, &stderr)
	if exit == 0 {
		t.Fatal("old broker appeared to support redemption")
	}
	probe := roomCLIRequest(t, requests)
	assertStatusEqual(t, "only capability probe", roomCLIString(probe, "pairing_op"), "status")
	assertStatusEqual(t, "no mutation", len(requests), 0)
	position, err := os.Stdin.Seek(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertStatusEqual(t, "secret not read", position, int64(0))
	assertStatusEqual(t, "secret not printed", strings.Contains(out.String()+stderr.String(), "739-281"), false)
}

func TestRedeemCLIAcceptanceInvalidCodeNeverSent(t *testing.T) {
	for _, code := range []string{"abc\n", strings.Repeat("7", 65) + "\n", "739281" + strings.Repeat(" ", 123) + "BAD\n", ""} {
		t.Run(code[:min(len(code), 3)], func(t *testing.T) {
			redeemCLIInput(t, code)
			path, requests := pairingCLISocket(t, func(map[string]any) map[string]any { return redeemCLIReply("", "", true) })
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var out, stderr bytes.Buffer
			exit := run(ctx, []string{"operator", "pair", "redeem", "host:443", "--socket", path}, &out, &stderr)
			if exit == 0 {
				t.Fatal("invalid code accepted")
			}
			for len(requests) > 0 {
				req := <-requests
				if req["pairing_op"] == "redeem" {
					t.Fatal("invalid code sent to broker")
				}
			}
		})
	}
}

func TestRedeemCLIAcceptanceWaitBoundsBlockedPoll(t *testing.T) {
	redeemCLIInput(t, "739-281\n")
	release := make(chan struct{})
	defer close(release)
	path, requests := pairingCLISocket(t, func(req map[string]any) map[string]any {
		if req["pairing_op"] == "redeem" {
			return redeemCLIReply(req["message_id"].(string), "intent", true)
		}
		if target, ok := req["pairing_target"].(string); ok && target != "" {
			<-release
			return redeemCLIReply(target, "awaiting_acceptance", true)
		}
		return redeemCLIReply("", "", true)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var out, stderr bytes.Buffer
	start := time.Now()
	exit := run(ctx, []string{"operator", "pair", "redeem", "host:443", "--socket", path, "--wait", "600ms", "--timeout", "5s"}, &out, &stderr)
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("--wait did not bound the blocked poll: %s", elapsed)
	}
	assertStatusEqual(t, "unfinished redemption exit", exit, 1)
	assertStatusEqual(t, "probe, redeem and blocked poll", len(requests), 3)
	assertStatusEqual(t, "honest unfinished state", strings.Contains(out.String(), "intent"), true)
	assertStatusEqual(t, "does not claim acceptance", strings.Contains(out.String(), "awaiting_acceptance"), false)
}

func TestRedeemCLIAcceptanceStatusDisplaysExactUnlistedOperation(t *testing.T) {
	path, requests := pairingCLISocket(t, func(map[string]any) map[string]any {
		return redeemCLIReply("older-unlisted-operation", "outcome_unknown", true)
	})
	var out, stderr bytes.Buffer
	exit := run(context.Background(), []string{"operator", "pair", "status", "--socket", path, "older-unlisted-operation"}, &out, &stderr)
	assertStatusEqual(t, "targeted status: "+stderr.String(), exit, 0)
	req := roomCLIRequest(t, requests)
	assertStatusEqual(t, "read only", roomCLIString(req, "pairing_op"), "status")
	assertStatusEqual(t, "exact target", roomCLIString(req, "pairing_target"), "older-unlisted-operation")
	for _, want := range []string{"older-unlisted-operation", "outcome_unknown", "host:443"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("targeted status omitted %q: %s", want, out.String())
		}
	}
}

func TestRedeemCLIAcceptanceListingDisplaysRedemptionsAndOmissions(t *testing.T) {
	path, _ := pairingCLISocket(t, func(map[string]any) map[string]any {
		resp := redeemCLIReply("listed-operation", "intent", true)
		snapshot := resp["pairing_result"].(map[string]any)
		snapshot["joins"] = []any{resp["pairing_join"]}
		snapshot["joins_total"], snapshot["joins_truncated"] = 20, true
		delete(resp, "pairing_join")
		return resp
	})
	var out, stderr bytes.Buffer
	exit := run(context.Background(), []string{"operator", "pair", "status", "--socket", path}, &out, &stderr)
	assertStatusEqual(t, "listing: "+stderr.String(), exit, 0)
	for _, want := range []string{"listed-operation", "intent", "host:443", "20"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("redemption listing omitted %q: %s", want, out.String())
		}
	}
}
