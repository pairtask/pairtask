package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestUnpairCLIAcceptanceFreshSnapshotEpochAndExactConfirmation(t *testing.T) {
	path, requests := unpairCLI(t, true)
	key := "sha256:" + strings.Repeat("a", 64)
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "peer", "unpair", key, "--confirm", key, "--message-id", "stable-unpair", "--socket", path}, &out, &stderr)
	assertStatusEqual(t, stderr.String(), code, 0)
	lookup := roomCLIRequest(t, requests)
	assertStatusEqual(t, "lookup before mutation", roomCLIString(lookup, "type"), protocol.TypeOperatorPairing)
	req := roomCLIRequest(t, requests)
	assertStatusEqual(t, "unpair operation", roomCLIString(req, "contact_op"), "unpair")
	assertStatusEqual(t, "full key", roomCLIString(req, "contact_key"), key)
	assertStatusEqual(t, "exact confirmation", roomCLIString(req, "contact_confirm"), key)
	assertStatusEqual(t, "seen snapshot epoch", req["contact_epoch"], any(float64(7)))
	assertStatusEqual(t, "stable ID", roomCLIString(req, "message_id"), "stable-unpair")
	assertStatusEqual(t, "epoch echoed", strings.Contains(strings.ToLower(out.String()), "epoch 7"), true)
}
func TestUnpairCLIAcceptanceExplicitEpochSurvivesJSONRequest(t *testing.T) {
	path, requests := unpairCLI(t, true)
	key := "sha256:" + strings.Repeat("a", 64)
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "peer", "unpair", key, "--confirm", key, "--epoch", "7", "--socket", path, "--json"}, &out, &stderr)
	assertStatusEqual(t, stderr.String(), code, 0)
	roomCLIRequest(t, requests)
	req := roomCLIRequest(t, requests)
	assertStatusEqual(t, "explicit epoch", req["contact_epoch"], any(float64(7)))
	if !json.Valid(out.Bytes()) {
		t.Fatalf("invalid JSON output: %s", out.String())
	}
}
func TestUnpairCLIAcceptancePrefixesNamesAndWrongConfirmNeverMutate(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	for _, args := range [][]string{{key[:20], "--confirm", key}, {"Peer", "--confirm", key}, {key, "--confirm", key[:20]}, {key, "--confirm", "sha256:" + strings.Repeat("b", 64)}, {key}, {key, "--confirm", key, "--epoch", "0"}} {
		path, requests := unpairCLI(t, true)
		argv := append([]string{"operator", "peer", "unpair"}, args...)
		argv = append(argv, "--socket", path)
		var out, stderr bytes.Buffer
		code := run(context.Background(), argv, &out, &stderr)
		if code == 0 {
			t.Fatalf("invalid unpair succeeded: %v", args)
		}
		unpairCLINoMutation(t, requests)
	}
}
func TestUnpairCLIAcceptanceOldBrokerGetsOnlyCapabilityProbe(t *testing.T) {
	path, requests := unpairCLI(t, false)
	key := "sha256:" + strings.Repeat("a", 64)
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "peer", "unpair", key, "--confirm", key, "--socket", path}, &out, &stderr)
	if code == 0 {
		t.Fatal("old broker appeared to support unpair")
	}
	if !strings.Contains(strings.ToLower(stderr.String()), "upgrade") {
		t.Fatalf("missing upgrade guidance: %s", stderr.String())
	}
	unpairCLINoMutation(t, requests)
}
