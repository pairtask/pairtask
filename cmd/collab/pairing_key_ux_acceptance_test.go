package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestPairingKeyUXCLIAcceptsDigestAndSchemeButRejectsSchemeAlone(t *testing.T) {
	key := "sha256:abcdef123456" + strings.Repeat("a", 52)
	for _, op := range []string{"accept", "reject"} {
		for _, typed := range []string{"abc", "sha256:abc", strings.TrimPrefix(key, "sha256:"), key, "sha256", "sha256:"} {
			t.Run(op+"/"+typed, func(t *testing.T) {
				path, requests := pairingCLISocket(t, func(map[string]any) map[string]any { return pairingCLIResponse("", []string{key}, 1) })
				var out, stderr bytes.Buffer
				code := run(context.Background(), []string{"operator", "pair", op, "--socket", path, typed}, &out, &stderr)
				assertStatusEqual(t, "list before mutation", roomCLIString(roomCLIRequest(t, requests), "pairing_op"), "list")
				if typed == "sha256" || typed == "sha256:" {
					assertStatusEqual(t, "scheme alone refused", code, 1)
					select {
					case req := <-requests:
						t.Fatalf("scheme alone sent mutation: %v", req)
					default:
					}
					return
				}
				assertStatusEqual(t, "resolution succeeds: "+stderr.String(), code, 0)
				req := roomCLIRequest(t, requests)
				assertStatusEqual(t, "correct operation", roomCLIString(req, "pairing_op"), op)
				assertStatusEqual(t, "full identity on wire", roomCLIString(req, "pairing_target"), key)
			})
		}
	}
}
