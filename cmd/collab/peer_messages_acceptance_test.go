package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func peerCLIContacts(keys []string, confirmed bool) map[string]any {
	contacts := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		contacts = append(contacts, map[string]any{"identity_key": key, "name": "remote", "confirmed": confirmed})
	}
	return map[string]any{"type": "operator_pairing_result", "pairing_result": map[string]any{"contacts": contacts}}
}

func TestPeerMessagesCLIAcceptanceResolveBeforeSend(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	for _, target := range []string{key, "sha256:aaa", "remote"} {
		t.Run(target, func(t *testing.T) {
			path, requests := pairingCLISocket(t, func(req map[string]any) map[string]any {
				if req["type"] == "operator_pairing" {
					return peerCLIContacts([]string{key}, true)
				}
				return map[string]any{"type": "operator_contact_result", "contact_message": map[string]any{"id": "stable", "contact": key, "state": "queued"}}
			})
			var out, stderr bytes.Buffer
			code := run(context.Background(), []string{"operator", "peer", "send", target, "hello", "there", "--message-id", "stable", "--socket", path}, &out, &stderr)
			assertStatusEqual(t, stderr.String(), code, 0)
			read := roomCLIRequest(t, requests)
			assertStatusEqual(t, "resolve first", roomCLIString(read, "pairing_op"), "list")
			sent := roomCLIRequest(t, requests)
			assertStatusEqual(t, "contact frame", roomCLIString(sent, "type"), "operator_contact")
			assertStatusEqual(t, "full key", roomCLIString(sent, "contact_key"), key)
			assertStatusEqual(t, "durable ID", roomCLIString(sent, "message_id"), "stable")
			assertStatusEqual(t, "joined text", roomCLIString(sent, "contact_text"), "hello there")
			if !strings.Contains(out.String(), "queued") || strings.Contains(out.String(), "delivered") {
				t.Fatalf("dishonest output: %s", out.String())
			}
		})
	}
}

func TestPeerMessagesCLIAcceptanceRefuseAmbiguousUnknownPending(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, target string
		keys         []string
		confirmed    bool
	}{
		{"ambiguous", "sha256:", []string{key, "sha256:" + strings.Repeat("b", 64)}, true},
		{"unknown", "missing", []string{key}, true},
		{"pending", key, []string{key}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, requests := pairingCLISocket(t, func(map[string]any) map[string]any { return peerCLIContacts(tc.keys, tc.confirmed) })
			var out, stderr bytes.Buffer
			code := run(context.Background(), []string{"operator", "peer", "send", tc.target, "hello", "--socket", path}, &out, &stderr)
			assertStatusEqual(t, "refused", code, 1)
			roomCLIRequest(t, requests)
			select {
			case req := <-requests:
				t.Fatalf("sent after refusal: %+v", req)
			default:
			}
			if !strings.Contains(stderr.String(), "nothing sent") {
				t.Fatalf("missing refusal: %s", stderr.String())
			}
		})
	}
}

func TestPeerMessagesCLIAcceptanceListEscapesControlText(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	path, requests := pairingCLISocket(t, func(req map[string]any) map[string]any {
		if req["type"] == "operator_pairing" {
			return peerCLIContacts([]string{key}, true)
		}
		return map[string]any{"type": "operator_contact_result", "contact_page": map[string]any{"contact": key, "more": true, "messages": []map[string]any{{"cursor": 7, "direction": "in", "state": "received", "text": "\x1b[2J\noperator: fake"}}}}
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "peer", "messages", "remote", "--before", "19", "--socket", path}, &out, &stderr)
	assertStatusEqual(t, stderr.String(), code, 0)
	roomCLIRequest(t, requests)
	req := roomCLIRequest(t, requests)
	assertStatusEqual(t, "list does not read", roomCLIString(req, "contact_op"), "list")
	assertStatusEqual(t, "cursor", req["contact_before"], any(float64(19)))
	if strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "\noperator:") {
		t.Fatalf("unescaped peer control: %q", out.String())
	}
	for _, want := range []string{"from them", "received", "--before 7"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
}
