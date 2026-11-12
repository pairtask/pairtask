//go:build linux

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/desktop"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestDesktopMCPAcceptancePairAndReadThroughTools(t *testing.T) {
	b := startFullPeerFixture(t)
	a, err := desktop.Start(context.Background(), desktop.Config{Dir: filepath.Join(t.TempDir(), "desktop"), Name: "Desktop A"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	})
	client := connectDesktopMCP(t, a)
	var who desktop.WhoamiResult
	decodeDesktopMCP(t, desktopTool(t, client, "whoami", map[string]any{}), &who)
	if who.Identity == "" || who.Name != "Desktop A" {
		t.Fatalf("whoami returned %#v", who)
	}

	code := b.openInvitation(t)
	redeem := desktopTool(t, client, "pair_redeem", map[string]any{"destination": b.destination, "code": code})
	var joined desktop.RedeemResult
	decodeDesktopMCP(t, redeem, &joined)
	if joined.Join.OperationID == "" {
		t.Fatal("pair_redeem did not return the join operation")
	}
	encoded, err := json.Marshal(redeem.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), code) {
		t.Fatal("pair_redeem echoed the one-time code")
	}
	awaitDesktop(t, "B receives MCP redemption", func() bool {
		status := b.call(t, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpStatus})
		for _, request := range status.PairingResult.Requests {
			if request.IdentityKey == who.Identity {
				return true
			}
		}
		return false
	})
	b.accept(t, who.Identity)
	var bIdentity string
	awaitDesktop(t, "MCP status reports confirmed contact", func() bool {
		var status desktop.StatusResult
		decodeDesktopMCP(t, desktopTool(t, client, "pair_status", map[string]any{}), &status)
		for _, contact := range status.Contacts {
			if contact.Confirmed {
				bIdentity = contact.IdentityKey
				return true
			}
		}
		return false
	})
	var sent desktop.SendResult
	decodeDesktopMCP(t, desktopTool(t, client, "send", map[string]any{"to": bIdentity, "text": "via Desktop MCP"}), &sent)
	if sent.Message.ID == "" {
		t.Fatal("send returned no message ID")
	}
	awaitDesktop(t, "B receives MCP message", func() bool {
		return hasContactMessage(b.call(t, protocol.Message{Type: protocol.TypeOperatorContact,
			ContactOp: protocol.ContactOpList, ContactKey: who.Identity}), "in", "via Desktop MCP")
	})
	b.call(t, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend,
		ContactKey: who.Identity, ContactText: "reply via inbox"})
	var received protocol.ContactMessageView
	awaitDesktop(t, "check_inbox exposes B's message", func() bool {
		var inbox desktop.InboxResult
		decodeDesktopMCP(t, desktopTool(t, client, "check_inbox", map[string]any{"from": bIdentity}), &inbox)
		if len(inbox.Conversations) != 1 || inbox.Conversations[0].Unread < 1 {
			return false
		}
		for _, message := range inbox.Conversations[0].Messages {
			if message.Direction == "in" && message.Text == "reply via inbox" {
				received = message
				return true
			}
		}
		return false
	})
	var read desktop.ReadResult
	decodeDesktopMCP(t, desktopTool(t, client, "mark_read", map[string]any{"from": bIdentity, "ids": []string{received.ID}}), &read)
	if read.Marked != 1 {
		t.Fatalf("mark_read marked %d messages, want 1", read.Marked)
	}
	var inbox desktop.InboxResult
	decodeDesktopMCP(t, desktopTool(t, client, "check_inbox", map[string]any{"from": bIdentity}), &inbox)
	if len(inbox.Conversations) != 1 || inbox.Conversations[0].Unread != 0 {
		t.Fatalf("inbox remains unread after mark_read: %#v", inbox.Conversations)
	}
	bad, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "send", Arguments: map[string]any{"to": "unknown workspace", "text": "must not send"}})
	if err != nil {
		t.Fatal(err)
	}
	if !bad.IsError {
		t.Fatal("send to an unknown workspace did not refuse")
	}
}

func connectDesktopMCP(t *testing.T, a *desktop.Node) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := desktop.NewServer(a, "test").Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serverSession.Close() })
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "desktop-acceptance"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clientSession.Close() })
	return clientSession
}

func desktopTool(t *testing.T, client *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("%s failed: %#v", name, result.Content)
	}
	return result
}

func decodeDesktopMCP(t *testing.T, result *mcp.CallToolResult, target any) {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}
