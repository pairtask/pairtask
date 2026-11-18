package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestSpaceConnectCommandAcceptanceAliasesAndTarget(t *testing.T) {
	key := "sha256:" + strings.Repeat("b", 64)
	for _, prefix := range [][]string{{"space"}, {"operator", "space"}} {
		t.Run(strings.Join(prefix, "-"), func(t *testing.T) {
			path, requests := pairingCLISocket(t, func(map[string]any) map[string]any {
				return map[string]any{"type": protocol.TypeOperatorSpaceResult, "space_result": map[string]any{"contact": map[string]any{"identity_key": key, "name": "Bob", "confirmed": true, "pairing_epoch": 1}}}
			})
			args := append(append([]string{}, prefix...), "connect", key, "--space", commandSpaceID, "--socket", path, "--json")
			var out, stderr bytes.Buffer
			code := run(context.Background(), args, &out, &stderr)
			spaceCommandCheck(t, code == 0, "connect failed: %s", stderr.String())
			spaceCommandCheck(t, json.Valid(out.Bytes()), "invalid JSON: %s", out.String())
			spaceCommandCheck(t, strings.Contains(out.String(), key), "contact missing: %s", out.String())
			req := decodeSpaceCLI(t, roomCLIRequest(t, requests))
			spaceCommandCheck(t, req.Op == protocol.SpaceOpConnect, "wrong op: %+v", req)
			spaceCommandCheck(t, req.Node == key, "wrong member: %+v", req)
			spaceCommandCheck(t, req.Space == commandSpaceID, "wrong space: %+v", req)
		})
	}
}

func TestSpaceConnectCommandAcceptanceOptionalSpaceAndRefusal(t *testing.T) {
	key := "sha256:" + strings.Repeat("b", 64)
	path, requests := pairingCLISocket(t, func(map[string]any) map[string]any {
		return map[string]any{"type": protocol.TypeError, "code": protocol.ErrSpace, "detail": "target is not an approved member"}
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"space", "connect", key, "--socket", path}, &out, &stderr)
	spaceCommandCheck(t, code != 0, "connect ignored refusal")
	spaceCommandCheck(t, strings.Contains(stderr.String(), "approved member"), "refusal missing: %s", stderr.String())
	req := decodeSpaceCLI(t, roomCLIRequest(t, requests))
	spaceCommandCheck(t, req.Space == "", "implicit space selection lost: %+v", req)
	spaceCommandCheck(t, req.Node == key, "target lost: %+v", req)
}

func TestSpaceConnectCommandAcceptanceRejectsMissingOrExtraTarget(t *testing.T) {
	key := "sha256:" + strings.Repeat("b", 64)
	for _, args := range [][]string{{"connect"}, {"connect", key, key}} {
		_, _, code := parseSpace(args, &bytes.Buffer{})
		spaceCommandCheck(t, code == 2, "malformed connect arguments accepted: %v", args)
	}
}
