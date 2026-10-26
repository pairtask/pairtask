package main

import (
	"bytes"
	"context"
	"testing"
)

func TestRoomDeleteCLIAcceptanceOperatorEndpointAndFailure(t *testing.T) {
	path, requests := roomCLISocket(t, roomCLIResponse())
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "room", "delete", "--socket", path, "--json", "project"}, &out, &stderr)
	assertStatusEqual(t, "delete succeeds: "+stderr.String(), code, 0)
	req := roomCLIRequest(t, requests)
	assertStatusEqual(t, "operator endpoint request", roomCLIString(req, "type"), "operator_room")
	assertStatusEqual(t, "delete operation", roomCLIString(req, "room_op"), "delete")
	assertStatusEqual(t, "exact room", roomCLIString(req, "room"), "project")
	assertStatusEqual(t, "no member", roomCLIString(req, "agent_id"), "")
	path, _ = roomCLISocket(t, map[string]any{"type": "error", "code": "invalid_room", "detail": "no such room"})
	out.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"operator", "room", "delete", "--socket", path, "missing"}, &out, &stderr)
	assertStatusEqual(t, "broker refusal exits nonzero", code, 1)
	if !bytes.Contains(stderr.Bytes(), []byte("no such room")) {
		t.Fatalf("missing refusal detail: %s", stderr.String())
	}
}

func TestRoomDeleteCLIAcceptanceRejectsMissingExtraAndInvalidRoom(t *testing.T) {
	for _, args := range [][]string{nil, {"alpha", "worker"}, {"square"}, {"bad room"}} {
		var stderr bytes.Buffer
		_, _, code := parseOperatorRoom(append([]string{"delete"}, args...), &stderr)
		if code != 2 {
			t.Fatalf("delete %q returned %d, want usage error: %s", args, code, stderr.String())
		}
	}
}
