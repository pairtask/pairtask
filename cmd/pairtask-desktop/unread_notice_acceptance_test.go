//go:build linux

package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/desktop"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestDesktopUnreadNoticeAcceptanceAcrossTools(t *testing.T) {
	f := newDesktopMCPFixture(t)
	assertDesktopNotice(t, desktopTool(t, f.client, "whoami", map[string]any{}), 0, "")

	bKey, joined := pairDesktopNoticePeer(t, f, f.b)
	assertDesktopNotice(t, joined, 0, "")
	nameDesktopContact(t, f, bKey, "Zulu")
	f.b.call(t, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend,
		ContactKey: f.identity, ContactText: "B private body"})
	awaitDesktopInbound(t, f, bKey, "B private body")
	awaitDesktopNotice(t, f, 1, "Zulu")
	assertDesktopNotice(t, desktopTool(t, f.client, "pair_status", map[string]any{}), 1, "Zulu")
	assertDesktopNotice(t, desktopTool(t, f.client, "send", map[string]any{"to": bKey, "text": "outbound is not unread"}), 1, "Zulu")

	c := startFullPeerFixture(t)
	cKey, joined := pairDesktopNoticePeer(t, f, c)
	assertDesktopNotice(t, joined, 1, "Zulu")
	nameDesktopContact(t, f, cKey, "Alpha")
	c.call(t, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend,
		ContactKey: f.identity, ContactText: "C private body"})
	awaitDesktopInbound(t, f, cKey, "C private body")
	awaitDesktopNotice(t, f, 2, "Alpha, Zulu")
	assertDesktopNotice(t, desktopTool(t, f.client, "pair_status", map[string]any{}), 2, "Alpha, Zulu")

	markDesktopNoticeRead(t, f, bKey, "B private body")
	awaitDesktopNotice(t, f, 1, "Alpha")
	markDesktopNoticeRead(t, f, cKey, "C private body")
	awaitDesktopNotice(t, f, 0, "")
	assertDesktopNotice(t, desktopTool(t, f.client, "pair_status", map[string]any{}), 0, "")
	assertDesktopNotice(t, desktopTool(t, f.client, "send", map[string]any{"to": bKey, "text": "still no unread"}), 0, "")
}

func awaitDesktopInbound(t *testing.T, f *desktopMCPFixture, contact, text string) {
	t.Helper()
	awaitDesktop(t, "Desktop receives inbound message", func() bool {
		var inbox desktop.InboxResult
		decodeDesktopMCP(t, desktopTool(t, f.client, "check_inbox", map[string]any{"from": contact}), &inbox)
		for _, conversation := range inbox.Conversations {
			for _, message := range conversation.Messages {
				if message.Direction == "in" && message.Text == text {
					return true
				}
			}
		}
		return false
	})
}

func pairDesktopNoticePeer(t *testing.T, f *desktopMCPFixture, peer *fullPeerFixture) (string, *mcp.CallToolResult) {
	t.Helper()
	status := peer.call(t, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpStatus})
	if status.PairingResult == nil || status.PairingResult.SelfIdentity == "" {
		t.Fatalf("peer identity missing: %#v", status)
	}
	key := status.PairingResult.SelfIdentity
	joined := desktopTool(t, f.client, "pair_redeem", map[string]any{"destination": peer.destination, "code": peer.openInvitation(t)})
	awaitDesktop(t, "peer receives Desktop redemption", func() bool {
		return hasPairingRequest(peer.call(t, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpStatus}), f.identity)
	})
	peer.accept(t, f.identity)
	awaitDesktop(t, "Desktop confirms peer", func() bool {
		var result desktop.StatusResult
		decodeDesktopMCP(t, desktopTool(t, f.client, "pair_status", map[string]any{}), &result)
		for _, contact := range result.Contacts {
			if contact.IdentityKey == key && contact.Confirmed {
				return true
			}
		}
		return false
	})
	return key, joined
}

func nameDesktopContact(t *testing.T, f *desktopMCPFixture, key, name string) {
	t.Helper()
	out := f.a.Call(context.Background(), protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpName,
		ContactKey: key, ContactText: name})
	if out.Type == protocol.TypeError {
		t.Fatalf("name contact %s: %s", key, out.Detail)
	}
}

func awaitDesktopNotice(t *testing.T, f *desktopMCPFixture, count int, from string) {
	t.Helper()
	awaitDesktop(t, "Desktop unread notice", func() bool {
		var got desktop.WhoamiResult
		decodeDesktopMCP(t, desktopTool(t, f.client, "whoami", map[string]any{}), &got)
		return got.Unread == count && got.UnreadFrom == from
	})
	assertDesktopNotice(t, desktopTool(t, f.client, "whoami", map[string]any{}), count, from)
}

func assertDesktopNotice(t *testing.T, result *mcp.CallToolResult, count int, from string) {
	t.Helper()
	var fields map[string]json.RawMessage
	decodeDesktopMCP(t, result, &fields)
	var got desktop.InboxNotice
	decodeDesktopMCP(t, result, &got)
	if got.Unread != count || got.UnreadFrom != from {
		t.Fatalf("unread notice = %#v, want count=%d from=%q", got, count, from)
	}
	if count == 0 {
		if _, present := fields["unread"]; present {
			t.Fatal("empty unread count was not omitted")
		}
		if _, present := fields["unread_from"]; present {
			t.Fatal("empty unread sender was not omitted")
		}
	}
}

func markDesktopNoticeRead(t *testing.T, f *desktopMCPFixture, contact, text string) {
	t.Helper()
	var inbox desktop.InboxResult
	decodeDesktopMCP(t, desktopTool(t, f.client, "check_inbox", map[string]any{"from": contact}), &inbox)
	if len(inbox.Conversations) != 1 {
		t.Fatalf("contact inbox = %#v", inbox.Conversations)
	}
	for _, message := range inbox.Conversations[0].Messages {
		if message.Direction == "in" && message.Text == text {
			var result desktop.ReadResult
			decodeDesktopMCP(t, desktopTool(t, f.client, "mark_read", map[string]any{"from": contact, "ids": []string{message.ID}}), &result)
			if result.Marked != 1 {
				t.Fatalf("mark_read marked %d messages, want 1", result.Marked)
			}
			return
		}
	}
	t.Fatalf("unread message %q missing from contact inbox", text)
}
