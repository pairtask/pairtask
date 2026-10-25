package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

func roomCLIResponse() map[string]any {
	return map[string]any{"type": "operator_room_result", "room_list": map[string]any{
		"rooms":       []any{map[string]any{"id": "project", "members": []string{"worker"}}},
		"rooms_total": 1, "rooms_truncated": false,
	}}
}

func roomCLISocket(t *testing.T, response map[string]any) (string, <-chan map[string]any) {
	t.Helper()
	requests := make(chan map[string]any, 1)
	path := statusSocket(t, func(c net.Conn) {
		var req map[string]any
		if json.NewDecoder(bufio.NewReader(c)).Decode(&req) != nil {
			return
		}
		requests <- req
		json.NewEncoder(c).Encode(response)
	})
	return path, requests
}

func roomCLIRequest(t *testing.T, requests <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case req := <-requests:
		return req
	case <-time.After(time.Second):
		t.Fatal("room command sent no request")
		return nil
	}
}

func TestRoomsCLIAcceptanceCommandsUseOperatorEndpoint(t *testing.T) {
	for _, tc := range []struct {
		command, positional []string
		op, agent, room     string
	}{
		{[]string{"list"}, nil, "list", "", ""},
		{[]string{"create"}, []string{"project"}, "create", "", "project"},
		{[]string{"add"}, []string{"project", "worker"}, "add", "worker", "project"},
		{[]string{"remove"}, []string{"project", "worker"}, "remove", "worker", "project"},
	} {
		t.Run(tc.op, func(t *testing.T) {
			path, requests := roomCLISocket(t, roomCLIResponse())
			args := append([]string{"operator", "room"}, tc.command...)
			args = append(args, "--socket", path, "--json")
			args = append(args, tc.positional...)
			var out, stderr bytes.Buffer
			assertStatusEqual(t, "room CLI success: "+stderr.String(), run(context.Background(), args, &out, &stderr), 0)
			req := roomCLIRequest(t, requests)
			assertStatusEqual(t, "operator-only operation", req["type"], any("operator_room"))
			assertStatusEqual(t, "room operation", req["room_op"], any(tc.op))
			assertStatusEqual(t, "agent argument", roomCLIString(req, "agent_id"), tc.agent)
			assertStatusEqual(t, "room argument", roomCLIString(req, "room"), tc.room)
			var reply map[string]any
			if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			if reply["room_list"] == nil {
				t.Fatal("JSON lost room membership result")
			}
		})
	}
}

func roomCLIString(req map[string]any, key string) string {
	value, _ := req[key].(string)
	return value
}

func TestRoomsCLIAcceptanceBroadcastPreservesExplicitScope(t *testing.T) {
	path, requests := roomCLISocket(t, map[string]any{"type": "ack", "stage": "accepted", "seq": 1})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "send", "--socket", path, "--all", "--room", "project", "hello"}, &out, &stderr)
	assertStatusEqual(t, "scoped send: "+stderr.String(), code, 0)
	req := roomCLIRequest(t, requests)
	assertStatusEqual(t, "scope sent", roomCLIString(req, "room"), "project")
	assertStatusEqual(t, "broadcast recipient", roomCLIString(req, "to"), "*")
}

func TestRoomsCLIAcceptanceRefusalIsVisibleAndNonzero(t *testing.T) {
	path, _ := roomCLISocket(t, map[string]any{"type": "error", "code": "invalid_room", "detail": "invalid room name"})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "room", "add", "--socket", path, "project", "worker"}, &out, &stderr)
	assertStatusEqual(t, "broker rejection exit", code, 1)
	assertStatusEqual(t, "reason visible", strings.Contains(stderr.String(), "invalid room name"), true)
}
