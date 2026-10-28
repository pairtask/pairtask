package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func settleCLIReply(id, state string, confirmed bool) map[string]any {
	resp := redeemCLIReply(id, state, true)
	snapshot := resp["pairing_result"].(map[string]any)
	snapshot["contacts"], snapshot["contacts_total"] = []any{}, 0
	resp["pairing_settle"] = map[string]any{"queued": 1, "already_queued": 0, "not_queued": 0}
	if confirmed {
		resp["pairing_join"].(map[string]any)["decision_confirmed_at"] = "2026-10-28T00:00:00Z"
	}
	return resp
}

func TestSettleCLIAcceptancePollsExactOperationUntilConfirmed(t *testing.T) {
	for _, final := range []string{"contact", "declined"} {
		t.Run(final, func(t *testing.T) {
			path, requests := pairingCLISocket(t, func(req map[string]any) map[string]any {
				target, _ := req["pairing_target"].(string)
				return settleCLIReply(target, final, target != "" && req["pairing_op"] == "status")
			})
			var out, stderr bytes.Buffer
			exit := run(context.Background(), []string{"operator", "pair", "settle", "--socket", path, "--wait", "2s", "old-unlisted-op"}, &out, &stderr)
			assertStatusEqual(t, "confirmed settlement: "+stderr.String(), exit, 0)
			assertStatusEqual(t, "availability probe", roomCLIString(roomCLIRequest(t, requests), "pairing_op"), "status")
			write := roomCLIRequest(t, requests)
			assertStatusEqual(t, "only mutation", roomCLIString(write, "pairing_op"), "settle")
			assertStatusEqual(t, "exact target", roomCLIString(write, "pairing_target"), "old-unlisted-op")
			poll := roomCLIRequest(t, requests)
			assertStatusEqual(t, "read-only poll", roomCLIString(poll, "pairing_op"), "status")
			assertStatusEqual(t, "poll retains full target", roomCLIString(poll, "pairing_target"), "old-unlisted-op")
			assertStatusEqual(t, "final state visible", strings.Contains(out.String(), final), true)
		})
	}
}

func TestSettleCLIAcceptanceWaitBoundsBlockedPollAndDoesNotClaimConfirmation(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	path, requests := pairingCLISocket(t, func(req map[string]any) map[string]any {
		target, _ := req["pairing_target"].(string)
		if target != "" && req["pairing_op"] == "status" {
			<-release
		}
		return settleCLIReply(target, "contact", false)
	})
	var out, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	exit := run(ctx, []string{"operator", "pair", "settle", "--socket", path, "--wait", "600ms", "--timeout", "5s", "one"}, &out, &stderr)
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("poll exceeded wait bound: %s", elapsed)
	}
	assertStatusEqual(t, "unfinished settlement exit", exit, 1)
	assertStatusEqual(t, "probe, mutate, poll", len(requests), 3)
	assertStatusEqual(t, "honest confirming status", strings.Contains(out.String(), "confirming"), true)
}

func TestSettleCLIAcceptanceOldOrUnavailableBrokerNeverMutated(t *testing.T) {
	for _, mode := range []string{"old", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			path, requests := pairingCLISocket(t, func(map[string]any) map[string]any {
				resp := settleCLIReply("", "", false)
				snap := resp["pairing_result"].(map[string]any)
				if mode == "old" {
					delete(snap, "contacts")
				} else {
					snap["redeem_available"] = false
				}
				return resp
			})
			var out, stderr bytes.Buffer
			exit := run(context.Background(), []string{"operator", "pair", "settle", "--socket", path, "one"}, &out, &stderr)
			assertStatusEqual(t, "unsupported settlement refused", exit, 1)
			assertStatusEqual(t, "read-only probe", roomCLIString(roomCLIRequest(t, requests), "pairing_op"), "status")
			assertStatusEqual(t, "no mutation", len(requests), 0)
		})
	}
}

func TestSettleCLIAcceptanceAllReportsOverflowWithoutPolling(t *testing.T) {
	path, requests := pairingCLISocket(t, func(map[string]any) map[string]any {
		resp := settleCLIReply("", "", false)
		resp["pairing_settle"] = map[string]any{"queued": 60, "already_queued": 4, "not_queued": 10}
		return resp
	})
	var out, stderr bytes.Buffer
	exit := run(context.Background(), []string{"operator", "pair", "settle", "--socket", path, "--json"}, &out, &stderr)
	assertStatusEqual(t, "all scheduled", exit, 0)
	assertStatusEqual(t, "probe and schedule only", len(requests), 2)
	var counts map[string]int
	if err := json.Unmarshal(out.Bytes(), &counts); err != nil {
		t.Fatal(err)
	}
	assertStatusEqual(t, "queued count", counts["queued"], 60)
	assertStatusEqual(t, "already queued count", counts["already_queued"], 4)
	assertStatusEqual(t, "overflow count", counts["not_queued"], 10)
}

func TestSettleCLIAcceptanceMissingExactJoinCannotReportSuccess(t *testing.T) {
	path, _ := pairingCLISocket(t, func(map[string]any) map[string]any { return settleCLIReply("", "", false) })
	var out, stderr bytes.Buffer
	exit := run(context.Background(), []string{"operator", "pair", "settle", "--socket", path, "missing-response"}, &out, &stderr)
	assertStatusEqual(t, "missing confirmation is not success", exit, 1)
}

func TestSettleCLIAcceptanceListingQuotesContactNamesAndConfirmation(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	path, _ := pairingCLISocket(t, func(map[string]any) map[string]any {
		resp := settleCLIReply("", "", false)
		snap := resp["pairing_result"].(map[string]any)
		snap["contacts"], snap["contacts_total"] = []any{map[string]any{"identity_key": key, "name": "operator\x1b[2J", "confirmed": false}}, 1
		snap["requests"], snap["requests_total"] = []any{map[string]any{"identity_key": key, "state": "accepted"}}, 1
		return resp
	})
	var out, stderr bytes.Buffer
	exit := run(context.Background(), []string{"operator", "pair", "status", "--socket", path}, &out, &stderr)
	assertStatusEqual(t, "listing succeeds", exit, 0)
	for _, want := range []string{"Contacts (1):", key, "confirming", `"operator`, "awaiting peer confirmation"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
	assertStatusEqual(t, "terminal escape absent", strings.ContainsRune(out.String()+stderr.String(), '\x1b'), false)
}
