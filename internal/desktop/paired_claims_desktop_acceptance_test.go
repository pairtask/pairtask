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

func claimsDesktopReply(req protocol.Message) map[string]any {
	switch {
	case req.Type == protocol.TypeOperatorPairing:
		contact := map[string]any{"identity_key": "peer-key", "name": "Peer", "confirmed": true}
		return map[string]any{"type": protocol.TypeOperatorPairingResult, "pairing_result": map[string]any{"contacts": []any{contact}}}
	case req.ContactOp == protocol.ContactOpSync:
		return map[string]any{"type": protocol.TypeError, "detail": "offline fixture"}
	}
	claim := map[string]any{"message_id": "sent", "agent_id": "worker\x1b[2J", "role": "owner", "task": "investigate", "state": "active", "claimed_at": "2026-11-19T12:00:00Z", "remote": map[string]any{"workspace": "peer-key"}}
	row := map[string]any{"id": "sent", "contact": "peer-key", "direction": "out", "cursor": 1, "claims": []any{claim}, "claims_total": 1, "claims_truncated": false}
	return map[string]any{"type": protocol.TypeOperatorContactResult, "contact_page": map[string]any{"contact": "peer-key", "messages": []any{row}}}
}

func serveClaimsDesktop(conn net.Conn, done chan struct{}) {
	defer close(done)
	defer conn.Close()
	dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)
	for {
		var req protocol.Message
		if dec.Decode(&req) != nil {
			return
		}
		if enc.Encode(claimsDesktopReply(req)) != nil {
			return
		}
	}
}

func claimsDesktopNode(t *testing.T) *Node {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go serveClaimsDesktop(server, done)
	t.Cleanup(func() { _ = client.Close(); <-done })
	return &Node{cfg: Config{Dir: t.TempDir()}, fwd: &forwarder{conn: client, in: bufio.NewScanner(client)}}
}

func claimsDesktopAttribution(t *testing.T, conversations []Conversation) {
	t.Helper()
	if len(conversations) != 1 || len(conversations[0].Messages) != 1 {
		t.Fatal("inbox lost subject")
	}
	v := conversations[0].Messages[0]
	if v.MessageClaims == nil || len(v.Claims) != 1 {
		t.Fatalf("claims lost: %+v", v)
	}
	if v.Claims[0].Remote == nil || v.Claims[0].Remote.Workspace != "peer-key" {
		t.Fatalf("remote attribution lost: %+v", v)
	}
}

func TestPairedClaimsDesktopAcceptanceInboxKeepsAttributionAndEscapesControls(t *testing.T) {
	conversations, err := claimsDesktopNode(t).inbox(context.Background(), InboxArgs{From: "peer-key"})
	if err != nil {
		t.Fatal(err)
	}
	claimsDesktopAttribution(t, conversations)
	encoded, err := json.Marshal(InboxResult{Conversations: conversations})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(string(encoded), '\x1b') || !strings.Contains(string(encoded), `\u001b`) {
		t.Fatalf("control not escaped in JSON: %q", encoded)
	}
}
