//go:build linux

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/desktop"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestDesktopMCPAcceptancePairAndReadThroughTools(t *testing.T) {
	f := newDesktopMCPFixture(t)
	f.redeem(t)
	f.exchange(t)
	f.readReply(t)
	f.refuseUnknownRecipient(t)
}

type desktopMCPFixture struct {
	*desktopPair
	client *mcp.ClientSession
}

func newDesktopMCPFixture(t *testing.T) *desktopMCPFixture {
	t.Helper()
	p := newDesktopPair(t)
	f := &desktopMCPFixture{desktopPair: p, client: connectDesktopMCP(t, p.a)}
	var who desktop.WhoamiResult
	decodeDesktopMCP(t, desktopTool(t, f.client, "whoami", map[string]any{}), &who)
	if who.Identity != p.identity || who.Name != "Desktop A" {
		t.Fatalf("whoami returned %#v", who)
	}
	return f
}

func (f *desktopMCPFixture) redeem(t *testing.T) {
	t.Helper()
	code := f.b.openInvitation(t)
	redeem := desktopTool(t, f.client, "pair_redeem", map[string]any{"destination": f.b.destination, "code": code})
	var joined desktop.RedeemResult
	decodeDesktopMCP(t, redeem, &joined)
	if joined.Join.OperationID == "" {
		t.Fatal("pair_redeem did not return the join operation")
	}
	assertCodeNotEchoed(t, redeem, code)
	awaitDesktop(t, "B receives MCP redemption", func() bool {
		status := f.b.call(t, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpStatus})
		return hasPairingRequest(status, f.identity)
	})
	f.b.accept(t, f.identity)
	awaitDesktop(t, "MCP status reports confirmed contact", func() bool {
		var status desktop.StatusResult
		decodeDesktopMCP(t, desktopTool(t, f.client, "pair_status", map[string]any{}), &status)
		f.bIdentity = confirmedContact(protocol.PairingSnapshot{Contacts: status.Contacts})
		return f.bIdentity != ""
	})
}

func assertCodeNotEchoed(t *testing.T, redeem *mcp.CallToolResult, code string) {
	t.Helper()
	encoded, err := json.Marshal(redeem.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), code) {
		t.Fatal("pair_redeem echoed the one-time code")
	}
}

func (f *desktopMCPFixture) exchange(t *testing.T) {
	t.Helper()
	var sent desktop.SendResult
	decodeDesktopMCP(t, desktopTool(t, f.client, "send", map[string]any{"to": f.bIdentity, "text": "via Desktop MCP"}), &sent)
	if sent.Message.ID == "" {
		t.Fatal("send returned no message ID")
	}
	awaitDesktop(t, "B receives MCP message", func() bool {
		return hasContactMessage(f.b.call(t, protocol.Message{Type: protocol.TypeOperatorContact,
			ContactOp: protocol.ContactOpList, ContactKey: f.identity}), "in", "via Desktop MCP")
	})
	f.b.call(t, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend,
		ContactKey: f.identity, ContactText: "reply via inbox"})
}

func (f *desktopMCPFixture) readReply(t *testing.T) {
	t.Helper()
	var received protocol.ContactMessageView
	awaitDesktop(t, "check_inbox exposes B's message", func() bool {
		inbox := f.inbox(t)
		received = unreadReply(inbox)
		return received.ID != ""
	})
	var read desktop.ReadResult
	decodeDesktopMCP(t, desktopTool(t, f.client, "mark_read", map[string]any{"from": f.bIdentity, "ids": []string{received.ID}}), &read)
	if read.Marked != 1 {
		t.Fatalf("mark_read marked %d messages, want 1", read.Marked)
	}
	inbox := f.inbox(t)
	if len(inbox.Conversations) != 1 || inbox.Conversations[0].Unread != 0 {
		t.Fatalf("inbox remains unread after mark_read: %#v", inbox.Conversations)
	}
}

func (f *desktopMCPFixture) inbox(t *testing.T) desktop.InboxResult {
	t.Helper()
	var inbox desktop.InboxResult
	decodeDesktopMCP(t, desktopTool(t, f.client, "check_inbox", map[string]any{"from": f.bIdentity}), &inbox)
	return inbox
}

func unreadReply(inbox desktop.InboxResult) protocol.ContactMessageView {
	if len(inbox.Conversations) != 1 || inbox.Conversations[0].Unread < 1 {
		return protocol.ContactMessageView{}
	}
	for _, message := range inbox.Conversations[0].Messages {
		if message.Direction == "in" && message.Text == "reply via inbox" {
			return message
		}
	}
	return protocol.ContactMessageView{}
}

func (f *desktopMCPFixture) refuseUnknownRecipient(t *testing.T) {
	t.Helper()
	bad, err := f.client.CallTool(context.Background(), &mcp.CallToolParams{Name: "send", Arguments: map[string]any{"to": "unknown workspace", "text": "must not send"}})
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
