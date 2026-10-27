package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pairingCLIResponse(code string, keys []string, total int) map[string]any {
	requests := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		requests = append(requests, map[string]any{"identity_key": key, "name": "workspace", "state": "pending_acceptance", "first_paired_at": "2026-10-27T12:00:00Z"})
	}
	result := map[string]any{"requests": requests, "requests_total": total, "requests_truncated": total > len(keys)}
	invitation := map[string]any{"id": "invitation-1", "expires_at": "2100-01-01T00:00:00Z", "attempts_left": 3}
	if code != "" {
		invitation["code"] = code
	}
	result["invitation"] = invitation
	return map[string]any{"type": "operator_pairing_result", "pairing_result": result}
}

func TestPairingCLIAcceptanceOpenStatusAndList(t *testing.T) {
	for _, op := range []string{"open", "status", "list"} {
		for _, asJSON := range []bool{false, true} {
			t.Run(op+map[bool]string{false: "-text", true: "-json"}[asJSON], func(t *testing.T) {
				code := ""
				if op == "open" {
					code = "739-281"
				}
				path, requests := roomCLISocket(t, pairingCLIResponse(code, nil, 0))
				args := []string{"operator", "pair", op, "--socket", path}
				if asJSON {
					args = append(args, "--json")
				}
				var out, stderr bytes.Buffer
				exit := run(context.Background(), args, &out, &stderr)
				assertStatusEqual(t, "pair CLI: "+stderr.String(), exit, 0)
				req := roomCLIRequest(t, requests)
				assertStatusEqual(t, "operator frame", roomCLIString(req, "type"), "operator_pairing")
				assertStatusEqual(t, "requested operation", roomCLIString(req, "pairing_op"), op)
				assertStatusEqual(t, "code never on stderr", strings.Contains(stderr.String(), "739-281"), false)
				assertStatusEqual(t, "code only in open output", strings.Contains(out.String(), "739-281"), op == "open")
				if asJSON && !json.Valid(out.Bytes()) {
					t.Fatal("invalid JSON pairing output")
				}
			})
		}
	}
}

// A scripted operator socket permits the read-then-mutate CLI flow without
// opening a real operator session or sending anything to the live broker.
func pairingCLISocket(t *testing.T, respond func(map[string]any) map[string]any) (string, <-chan map[string]any) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pairing-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "operator.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan map[string]any, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.SetDeadline(time.Now().Add(2 * time.Second))
			var request map[string]any
			if json.NewDecoder(bufio.NewReader(conn)).Decode(&request) == nil {
				requests <- request
				json.NewEncoder(conn).Encode(respond(request))
			}
			conn.Close()
		}
	}()
	t.Cleanup(func() { ln.Close(); <-done })
	return path, requests
}

func TestPairingCLIAcceptanceResolvesTargetsBeforeMutation(t *testing.T) {
	for _, tc := range []struct{ op, read, arg, target string }{
		{"cancel", "status", "", "invitation-1"},
		{"accept", "list", "abc", "abcdef-identity"},
		{"reject", "list", "abc", "abcdef-identity"},
	} {
		t.Run(tc.op, func(t *testing.T) {
			path, requests := pairingCLISocket(t, func(map[string]any) map[string]any {
				return pairingCLIResponse("", []string{"abcdef-identity", "other-identity"}, 2)
			})
			args := []string{"operator", "pair", tc.op, "--socket", path}
			if tc.arg != "" {
				args = append(args, tc.arg)
			}
			var out, stderr bytes.Buffer
			exit := run(context.Background(), args, &out, &stderr)
			assertStatusEqual(t, "target resolution: "+stderr.String(), exit, 0)
			read := roomCLIRequest(t, requests)
			assertStatusEqual(t, "read before decision", roomCLIString(read, "pairing_op"), tc.read)
			write := roomCLIRequest(t, requests)
			assertStatusEqual(t, "operator mutation", roomCLIString(write, "type"), "operator_pairing")
			assertStatusEqual(t, "mutation operation", roomCLIString(write, "pairing_op"), tc.op)
			assertStatusEqual(t, "full resolved target", roomCLIString(write, "pairing_target"), tc.target)
		})
	}
}

func TestPairingCLIAcceptanceAmbiguousAndPartialPrefixesRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		keys  []string
		total int
	}{
		{"ambiguous", []string{"abc-one", "abc-two"}, 2},
		{"missing", []string{"other"}, 1},
		{"partial", []string{"abc-one"}, 65},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, requests := pairingCLISocket(t, func(map[string]any) map[string]any { return pairingCLIResponse("", tc.keys, tc.total) })
			var out, stderr bytes.Buffer
			exit := run(context.Background(), []string{"operator", "pair", "accept", "--socket", path, "abc"}, &out, &stderr)
			assertStatusEqual(t, "prefix refusal", exit, 1)
			assertStatusEqual(t, "listing consulted", roomCLIString(roomCLIRequest(t, requests), "pairing_op"), "list")
			select {
			case request := <-requests:
				t.Fatalf("refused prefix sent mutation: %v", request)
			default:
			}
			assertStatusEqual(t, "refusal diagnostic present", stderr.Len() > 0, true)
		})
	}
}

func TestPairingCLIAcceptanceBrokerRefusalAndTerminalControls(t *testing.T) {
	path, _ := roomCLISocket(t, map[string]any{"type": "error", "code": "pairing_refused", "detail": "invitation_expired"})
	var out, stderr bytes.Buffer
	exit := run(context.Background(), []string{"operator", "pair", "open", "--socket", path}, &out, &stderr)
	assertStatusEqual(t, "broker refusal exit", exit, 1)
	assertStatusEqual(t, "broker reason visible", strings.Contains(stderr.String(), "invitation_expired"), true)
	assertStatusEqual(t, "error does not print result", out.Len(), 0)
	response := pairingCLIResponse("", []string{"identity\x1b[2J"}, 1)
	path, _ = roomCLISocket(t, response)
	out.Reset()
	stderr.Reset()
	exit = run(context.Background(), []string{"operator", "pair", "list", "--socket", path}, &out, &stderr)
	assertStatusEqual(t, "list success", exit, 0)
	assertStatusEqual(t, "terminal controls escaped", strings.ContainsRune(out.String()+stderr.String(), '\x1b'), false)
}

func TestPairingCLIAcceptanceDefaultListAndExplicitCancel(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		op, target string
	}{
		{nil, "list", ""},
		{[]string{"cancel", "invitation-1"}, "cancel", "invitation-1"},
	} {
		t.Run(tc.op, func(t *testing.T) {
			path, requests := roomCLISocket(t, pairingCLIResponse("", nil, 0))
			args := []string{"operator", "pair"}
			if len(tc.args) > 0 {
				args = append(args, tc.args[0])
			}
			args = append(args, "--socket", path)
			if len(tc.args) > 1 {
				args = append(args, tc.args[1:]...)
			}
			var out, stderr bytes.Buffer
			exit := run(context.Background(), args, &out, &stderr)
			assertStatusEqual(t, "pair shorthand: "+stderr.String(), exit, 0)
			req := roomCLIRequest(t, requests)
			assertStatusEqual(t, "shorthand operation", roomCLIString(req, "pairing_op"), tc.op)
			assertStatusEqual(t, "explicit target", roomCLIString(req, "pairing_target"), tc.target)
		})
	}
}

func TestPairingCLIAcceptanceEmptyDecisionKeyIsUsageError(t *testing.T) {
	for _, op := range []string{"accept", "reject"} {
		for _, key := range []string{"", "   "} {
			var out, stderr bytes.Buffer
			exit := run(context.Background(), []string{"operator", "pair", op, "--socket", filepath.Join(t.TempDir(), "absent.sock"), key}, &out, &stderr)
			assertStatusEqual(t, "empty key rejected before connection", exit, 2)
			assertStatusEqual(t, "usage refusal leaves stdout empty", out.Len(), 0)
		}
	}
}

func TestPairingIdentityCLIAcceptanceShowsFingerprintOrRecoveryError(t *testing.T) {
	fingerprint := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct{ field, value string }{{"self_identity", fingerprint}, {"identity_error", "identity_missing"}} {
		t.Run(tc.field, func(t *testing.T) {
			response := pairingCLIResponse("", nil, 0)
			response["pairing_result"].(map[string]any)[tc.field] = tc.value
			path, _ := roomCLISocket(t, response)
			var out, stderr bytes.Buffer
			exit := run(context.Background(), []string{"operator", "pair", "status", "--socket", path}, &out, &stderr)
			assertStatusEqual(t, "status remains available", exit, 0)
			visible := strings.ToLower(out.String() + stderr.String())
			if tc.field == "self_identity" {
				assertStatusEqual(t, "fingerprint visible", strings.Contains(visible, fingerprint), true)
			} else {
				assertStatusEqual(t, "identity recovery state visible", strings.Contains(visible, "identity") && strings.Contains(visible, "missing"), true)
			}
		})
	}
}
