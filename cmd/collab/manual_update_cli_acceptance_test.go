package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

func manualUpdateCLIArgs(path string) []string {
	return []string{"operator", "update-notice", "set", "--state", "update_available", "--action", "upgrade_app",
		"--app-release", "workspace-v0.0.0-build.99.1", "--app-commit", strings.Repeat("a", 40),
		"--host-release", "workspace-v0.0.0-build.100.1", "--available-release", "workspace-v0.0.0-build.100.1",
		"--checked-at", "2026-11-08T12:00:00Z", "--observed-at", "2026-11-08T12:00:01Z", "--json", "--socket", path}
}

func TestManualUpdateCLIAcceptanceMapsLiteralFlagsToOneShotWire(t *testing.T) {
	requests := make(chan map[string]json.RawMessage, 1)
	path := statusSocket(t, func(c net.Conn) {
		var request map[string]json.RawMessage
		if json.NewDecoder(c).Decode(&request) != nil {
			return
		}
		requests <- request
		json.NewEncoder(c).Encode(map[string]any{"type": "operator_update_notice_result", "update_notice": request["update_notice"]})
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), manualUpdateCLIArgs(path), &out, &stderr)
	assertStatusEqual(t, "notice CLI exit", code, 0)
	assertStatusEqual(t, "JSON only stdout", json.Valid(out.Bytes()), true)
	select {
	case request := <-requests:
		assertStatusEqual(t, "one-shot request", string(request["type"]), `"operator_update_notice"`)
		var notice map[string]string
		if err := json.Unmarshal(request["update_notice"], &notice); err != nil {
			t.Fatal(err)
		}
		assertStatusEqual(t, "literal action", notice["action"], "upgrade_app")
		assertStatusEqual(t, "literal observed timestamp", notice["observed_at"], "2026-11-08T12:00:01Z")
		assertStatusEqual(t, "literal app commit", notice["app_commit"], strings.Repeat("a", 40))
	case <-time.After(time.Second):
		t.Fatal("notice CLI sent no request")
	}
}

func TestManualUpdateCLIAcceptanceOlderBrokerErrorRemainsFailure(t *testing.T) {
	path := statusSocket(t, func(c net.Conn) {
		var request map[string]json.RawMessage
		if json.NewDecoder(c).Decode(&request) == nil {
			json.NewEncoder(c).Encode(map[string]any{"type": "error", "code": "unknown_type", "detail": "unsupported notice"})
		}
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), manualUpdateCLIArgs(path), &out, &stderr)
	assertStatusEqual(t, "old broker error exit", code, 1)
	assertStatusEqual(t, "old broker failure visible", strings.Contains(stderr.String(), "unsupported notice"), true)
}
