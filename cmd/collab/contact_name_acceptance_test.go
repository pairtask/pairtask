package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestContactNameAcceptanceOldBrokerNeverReceivesMutation(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	path, requests := pairingCLISocket(t, func(req map[string]any) map[string]any {
		if req["type"] == protocol.TypeOperatorContact {
			return map[string]any{"type": protocol.TypeError, "code": protocol.ErrMalformedFrame}
		}
		return map[string]any{"type": protocol.TypeOperatorPairingResult, "pairing_result": map[string]any{
			"contacts": []any{map[string]any{"identity_key": key, "name": "Peer", "confirmed": true}},
		}}
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "peer", "name", key, "Office", "--socket", path}, &out, &stderr)
	if code == 0 {
		t.Fatal("old broker appeared to support local names")
	}
	for len(requests) > 0 {
		req := <-requests
		if req["type"] == protocol.TypeOperatorContact {
			t.Fatalf("old broker received mutation: %v", req)
		}
	}
}

func TestContactNameAcceptanceResolverNeverPrefersAliasOverAnotherIdentity(t *testing.T) {
	rows := []protocol.PairingContactView{
		{IdentityKey: "sha256:aaaa", Name: "Peer A", LocalName: "Office", Confirmed: true},
		{IdentityKey: "sha256:bbbb", Name: "Office", LocalName: "Laptop", Confirmed: true},
	}
	if key, err := matchContact(rows, "Office"); err == nil || key != "" {
		t.Fatalf("ambiguous alias/peer name routed to %q: %v", key, err)
	}
	key, err := matchContact(rows, "Laptop")
	if err != nil || key != rows[1].IdentityKey {
		t.Fatalf("unique alias did not resolve: %q %v", key, err)
	}
	key, err = matchContact(rows, rows[0].IdentityKey)
	if err != nil || key != rows[0].IdentityKey {
		t.Fatalf("exact identity did not resolve: %q %v", key, err)
	}
}
