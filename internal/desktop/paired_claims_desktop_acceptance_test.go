package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPairedClaimsDesktopAcceptanceInboxKeepsAttributionAndEscapesControls(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		dec, enc := json.NewDecoder(server), json.NewEncoder(server)
		for {
			var req protocol.Message
			if dec.Decode(&req) != nil {
				return
			}
			var out map[string]any
			switch {
			case req.Type == protocol.TypeOperatorPairing:
				out = map[string]any{"type": protocol.TypeOperatorPairingResult, "pairing_result": map[string]any{"contacts": []any{map[string]any{"identity_key": "peer-key", "name": "Peer", "confirmed": true}}}}
			case req.ContactOp == protocol.ContactOpSync:
				out = map[string]any{"type": protocol.TypeError, "detail": "offline fixture"}
			default:
				claim := map[string]any{"message_id": "sent", "agent_id": "worker\x1b[2J", "role": "owner", "task": "investigate", "state": "active", "claimed_at": "2026-11-19T12:00:00Z", "remote": map[string]any{"workspace": "peer-key"}}
				row := map[string]any{"id": "sent", "contact": "peer-key", "direction": "out", "cursor": 1, "claims": []any{claim}, "claims_total": 1, "claims_truncated": false}
				out = map[string]any{"type": protocol.TypeOperatorContactResult, "contact_page": map[string]any{"contact": "peer-key", "messages": []any{row}}}
			}
			if enc.Encode(out) != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = client.Close(); <-done })
	n := &Node{cfg: Config{Dir: t.TempDir()}, fwd: &forwarder{conn: client, in: bufio.NewScanner(client)}}
	conversations, err := n.inbox(context.Background(), InboxArgs{From: "peer-key"})
	if err != nil {
		t.Fatal(err)
	}
	if len(conversations) != 1 || len(conversations[0].Messages) != 1 {
		t.Fatal("inbox lost subject")
	}
	v := conversations[0].Messages[0]
	if v.ClaimPage == nil || len(v.Claims) != 1 || v.Claims[0].Remote == nil || v.Claims[0].Remote.Workspace != "peer-key" {
		t.Fatalf("remote claim attribution lost: %+v", v)
	}
	encoded, err := json.Marshal(InboxResult{Conversations: conversations})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(string(encoded), '\x1b') || !strings.Contains(string(encoded), `\u001b`) {
		t.Fatalf("control not escaped in JSON: %q", encoded)
	}
}
