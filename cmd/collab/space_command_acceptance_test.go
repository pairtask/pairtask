package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

const commandSpaceID = "0123456789abcdef0123456789abcdef"

func spaceCLIResult() map[string]any {
	return map[string]any{"type": protocol.TypeOperatorSpaceResult, "space_result": map[string]any{"pins": []any{map[string]any{"space": commandSpaceID, "owner": "sha256:" + strings.Repeat("a", 64), "directory": "https://directory.example"}}}}
}
func decodeSpaceCLI(t *testing.T, raw map[string]any) protocol.SpaceRequest {
	t.Helper()
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var msg protocol.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != protocol.TypeOperatorSpace || msg.SpaceRequest == nil {
		t.Fatalf("wrong space request: %+v", msg)
	}
	return *msg.SpaceRequest
}

func TestSpaceCommandAcceptanceTopLevelAndOperatorAlias(t *testing.T) {
	for _, prefix := range [][]string{{"space"}, {"operator", "space"}} {
		t.Run(strings.Join(prefix, "-"), func(t *testing.T) {
			path, requests := pairingCLISocket(t, func(map[string]any) map[string]any { return spaceCLIResult() })
			args := append(append([]string{}, prefix...), "create", "--pairbook", "https://directory.example", "--id", commandSpaceID, "--name", "Office", "--socket", path, "--json")
			var out, stderr bytes.Buffer
			code := run(context.Background(), args, &out, &stderr)
			if code != 0 || !json.Valid(out.Bytes()) {
				t.Fatalf("space create failed: %d %q %q", code, out.String(), stderr.String())
			}
			req := decodeSpaceCLI(t, roomCLIRequest(t, requests))
			if req.Op != protocol.SpaceOpCreate || req.Directory != "https://directory.example" || req.ID != commandSpaceID || req.Name != "Office" {
				t.Fatalf("wrong create arguments: %+v", req)
			}
		})
	}
}

func TestSpaceCommandAcceptanceInviteAndPublishArguments(t *testing.T) {
	const link = "https://directory.example/v0/spaces/0123456789abcdef0123456789abcdef#token=SECRET&owner=PIN"
	path, requests := pairingCLISocket(t, func(map[string]any) map[string]any {
		return map[string]any{"type": protocol.TypeOperatorSpaceResult, "space_result": map[string]any{"invite": link}}
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"space", "invite", "--space", commandSpaceID, "--valid", "2h", "--socket", path}, &out, &stderr)
	if code != 0 || !strings.Contains(out.String(), link) {
		t.Fatalf("invite not printed: %d %q %q", code, out.String(), stderr.String())
	}
	req := decodeSpaceCLI(t, roomCLIRequest(t, requests))
	if req.Op != protocol.SpaceOpInvite || req.Space != commandSpaceID || req.ValidFor != (2*time.Hour).Milliseconds() {
		t.Fatalf("wrong invite arguments: %+v", req)
	}
	out.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"space", "publish", "a.example:7445", "b.example:7445", "--space", commandSpaceID, "--socket", path}, &out, &stderr)
	if code != 0 {
		t.Fatalf("publish failed: %s", stderr.String())
	}
	req = decodeSpaceCLI(t, roomCLIRequest(t, requests))
	if req.Op != protocol.SpaceOpPublish || len(req.Addresses) != 2 || req.Addresses[0] != "a.example:7445" {
		t.Fatalf("wrong publish arguments: %+v", req)
	}
}

func TestSpaceCommandAcceptanceJoinAndBrokerRefusal(t *testing.T) {
	const link = "https://directory.example/v0/spaces/0123456789abcdef0123456789abcdef#token=SECRET&owner=PIN"
	path, requests := pairingCLISocket(t, func(map[string]any) map[string]any {
		return map[string]any{"type": protocol.TypeError, "code": protocol.ErrSpace, "detail": "space owner conflicts with saved pin"}
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"space", "join", link, "--name", "Alice", "--socket", path}, &out, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "conflicts") {
		t.Fatalf("broker refusal lost: %d %q %q", code, out.String(), stderr.String())
	}
	req := decodeSpaceCLI(t, roomCLIRequest(t, requests))
	if req.Op != protocol.SpaceOpJoin || req.Link != link || req.Name != "Alice" {
		t.Fatalf("wrong join arguments: %+v", req)
	}
}

func TestSpaceCommandAcceptanceRejectsSubmillisecondValidity(t *testing.T) {
	_, _, valid := parseSpace([]string{"invite", "--valid", "1ms", "--socket", "/tmp/pairbook-cli-test.sock"}, &bytes.Buffer{})
	if valid != -1 {
		t.Fatalf("positive duration control failed: %d", valid)
	}
	_, _, code := parseSpace([]string{"invite", "--valid", "1ns", "--socket", "/tmp/pairbook-cli-test.sock"}, &bytes.Buffer{})
	if code != 2 {
		t.Fatalf("sub-millisecond validity became a default duration: code=%d", code)
	}
}
