package main

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
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
	spaceCommandCheck(t, err == nil, "%v", err)
	var msg protocol.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatal(err)
	}
	spaceCommandCheck(t, msg.Type == protocol.TypeOperatorSpace, "wrong space request: %+v", msg)
	spaceCommandCheck(t, msg.SpaceRequest != nil, "wrong space request: %+v", msg)
	return *msg.SpaceRequest
}

func TestSpaceCommandAcceptanceTopLevelAndOperatorAlias(t *testing.T) {
	for _, prefix := range [][]string{{"space"}, {"operator", "space"}} {
		t.Run(strings.Join(prefix, "-"), func(t *testing.T) {
			path, requests := pairingCLISocket(t, func(map[string]any) map[string]any { return spaceCLIResult() })
			args := append(append([]string{}, prefix...), "v0", "create", "--pairbook", "https://directory.example", "--id", commandSpaceID, "--name", "Office", "--socket", path, "--json")
			var out, stderr bytes.Buffer
			code := run(context.Background(), args, &out, &stderr)
			spaceCommandCheck(t, code == 0, "space create failed: %d %q %q", code, out.String(), stderr.String())
			spaceCommandCheck(t, json.Valid(out.Bytes()), "space create failed: %d %q %q", code, out.String(), stderr.String())
			req := decodeSpaceCLI(t, roomCLIRequest(t, requests))
			spaceCommandCheck(t, req.Op == protocol.SpaceOpCreate, "wrong create arguments: %+v", req)
			spaceCommandCheck(t, req.Directory == "https://directory.example", "wrong create arguments: %+v", req)
			spaceCommandCheck(t, req.ID == commandSpaceID, "wrong create arguments: %+v", req)
			spaceCommandCheck(t, req.Name == "Office", "wrong create arguments: %+v", req)
		})
	}
}

func TestSpaceCommandAcceptanceInviteAndPublishArguments(t *testing.T) {
	const link = "https://directory.example/v0/spaces/0123456789abcdef0123456789abcdef#token=SECRET&owner=PIN"
	path, requests := pairingCLISocket(t, func(map[string]any) map[string]any {
		return map[string]any{"type": protocol.TypeOperatorSpaceResult, "space_result": map[string]any{"invite": link}}
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"space", "v0", "invite", "--space", commandSpaceID, "--valid", "2h", "--socket", path}, &out, &stderr)
	spaceCommandCheck(t, code == 0, "invite not printed: %d %q %q", code, out.String(), stderr.String())
	spaceCommandCheck(t, strings.Contains(out.String(), link), "invite not printed: %d %q %q", code, out.String(), stderr.String())
	req := decodeSpaceCLI(t, roomCLIRequest(t, requests))
	spaceCommandCheck(t, req.Op == protocol.SpaceOpInvite, "wrong invite arguments: %+v", req)
	spaceCommandCheck(t, req.Space == commandSpaceID, "wrong invite arguments: %+v", req)
	spaceCommandCheck(t, req.ValidFor == (2*time.Hour).Milliseconds(), "wrong invite arguments: %+v", req)
	out.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"space", "v0", "publish", "a.example:7445", "b.example:7445", "--space", commandSpaceID, "--socket", path}, &out, &stderr)
	spaceCommandCheck(t, code == 0, "publish failed: %s", stderr.String())
	req = decodeSpaceCLI(t, roomCLIRequest(t, requests))
	spaceCommandCheck(t, req.Op == protocol.SpaceOpPublish, "wrong publish arguments: %+v", req)
	spaceCommandCheck(t, len(req.Addresses) == 2, "wrong publish arguments: %+v", req)
	spaceCommandCheck(t, req.Addresses[0] == "a.example:7445", "wrong publish arguments: %+v", req)
}

func TestSpaceCommandAcceptanceV1Publish(t *testing.T) {
	path, requests := pairingCLISocket(t, func(map[string]any) map[string]any { return spaceCLIResult() })
	var out, stderr bytes.Buffer
	args := []string{"space", "publish", "a.example:7445", "b.example:7445", "--space", commandSpaceID, "--socket", path}
	code := run(context.Background(), args, &out, &stderr)
	spaceCommandCheck(t, code == 0, "v1 publish failed: %s", stderr.String())
	lookup := decodeSpaceCLI(t, roomCLIRequest(t, requests))
	spaceCommandCheck(t, lookup.Op == protocol.SpaceOpList, "expected pin lookup: %+v", lookup)
	req := decodeSpaceCLI(t, roomCLIRequest(t, requests))
	spaceCommandCheck(t, req.Op == protocol.SpaceOpMapAddress && req.Space == commandSpaceID, "wrong v1 publish route: %+v", req)
	spaceCommandCheck(t, req.ValidFor == (24*time.Hour).Milliseconds(), "wrong v1 publish validity: %+v", req)
	spaceCommandCheck(t, slices.Equal(req.Addresses, []string{"a.example:7445", "b.example:7445"}), "wrong v1 publish endpoints: %+v", req)
}

func TestSpaceCommandAcceptanceJoinAndBrokerRefusal(t *testing.T) {
	const link = "https://directory.example/v0/spaces/0123456789abcdef0123456789abcdef#token=SECRET&owner=PIN"
	path, requests := pairingCLISocket(t, func(map[string]any) map[string]any {
		return map[string]any{"type": protocol.TypeError, "code": protocol.ErrSpace, "detail": "space owner conflicts with saved pin"}
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"space", "v0", "join", link, "--name", "Alice", "--socket", path}, &out, &stderr)
	spaceCommandCheck(t, code != 0, "broker refusal lost: %d %q %q", code, out.String(), stderr.String())
	spaceCommandCheck(t, strings.Contains(stderr.String(), "conflicts"), "broker refusal lost: %d %q %q", code, out.String(), stderr.String())
	req := decodeSpaceCLI(t, roomCLIRequest(t, requests))
	spaceCommandCheck(t, req.Op == protocol.SpaceOpJoin, "wrong join arguments: %+v", req)
	spaceCommandCheck(t, req.Link == link, "wrong join arguments: %+v", req)
	spaceCommandCheck(t, req.Name == "Alice", "wrong join arguments: %+v", req)
}

func TestSpaceCommandAcceptanceRejectsSubmillisecondValidity(t *testing.T) {
	_, _, valid := parseSpace([]string{"invite", "--valid", "1ms", "--socket", "/tmp/pairbook-cli-test.sock"}, &bytes.Buffer{})
	spaceCommandCheck(t, valid == -1, "positive duration control failed: %d", valid)
	_, _, code := parseSpace([]string{"invite", "--valid", "1ns", "--socket", "/tmp/pairbook-cli-test.sock"}, &bytes.Buffer{})
	spaceCommandCheck(t, code == 2, "sub-millisecond validity became a default duration: code=%d", code)
}

func spaceCommandCheck(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}

func TestSpaceCommandAcceptanceV1PublishValidity(t *testing.T) {
	for _, duration := range []string{"0s", "500us", "-500us", "24h1ns"} {
		t.Run(duration, func(t *testing.T) {
			path, requests := pairingCLISocket(t, func(map[string]any) map[string]any { return spaceCLIResult() })
			var out, stderr bytes.Buffer
			code := run(context.Background(), []string{"space", "publish", "a.example:7445", "--valid", duration, "--space", commandSpaceID, "--socket", path}, &out, &stderr)
			if code == 0 {
				t.Fatalf("accepted invalid validity %s", duration)
			}
			select {
			case <-requests:
				t.Fatal("invalid validity reached broker")
			default:
			}
		})
	}
}
