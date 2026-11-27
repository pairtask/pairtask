//go:build linux

package main

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/desktop"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestDesktopPairAddressAcceptanceMovesExistingContactWithoutRePairing(t *testing.T) {
	f := newDesktopMCPFixture(t)
	f.redeem(t)
	f.exchange(t)
	before := desktopContact(t, f, f.bIdentity)
	joinsBefore := desktopStatus(t, f).Joins
	if !before.Dials || before.Destination != f.b.destination {
		t.Fatalf("fixture must dial the original address: %+v", before)
	}
	newAddress := f.b.alternateAddress(t)
	moved := moveDesktopAddress(t, f, newAddress)
	assertMovedIdentity(t, moved.Contact, before, newAddress)
	assertMovedIdentity(t, desktopContact(t, f, f.bIdentity), before, newAddress)
	if joins := desktopStatus(t, f).Joins; !sameJoinIDs(joinsBefore, joins) {
		t.Fatalf("address move started a new invitation: before=%+v after=%+v", joinsBefore, joins)
	}
	conversations := f.inbox(t).Conversations
	if len(conversations) != 1 || !containsDesktopHistory(conversations[0].Messages, "via Desktop MCP") {
		t.Fatalf("address move lost message history: %+v", conversations)
	}
	assertDesktopSockets(t, f.dir)
}

func moveDesktopAddress(t *testing.T, f *desktopMCPFixture, destination string) desktop.AddressResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := f.client.CallTool(ctx, &mcp.CallToolParams{Name: "pair_address", Arguments: map[string]any{
		"contact": f.bIdentity, "destination": destination,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("pair_address failed: %s", desktopError(result))
	}
	var moved desktop.AddressResult
	decodeDesktopMCP(t, result, &moved)
	return moved
}

func assertMovedIdentity(t *testing.T, got, before protocol.PairingContactView, destination string) {
	t.Helper()
	if got.IdentityKey != before.IdentityKey {
		t.Fatalf("address change lost the paired identity: %+v", got)
	}
	if got.Destination != destination {
		t.Fatalf("address change did not persist the new destination: %+v", got)
	}
	if !got.Confirmed {
		t.Fatalf("address change lost confirmation: %+v", got)
	}
	if got.Name != before.Name {
		t.Fatalf("address change renamed the contact: %+v", got)
	}
}

func TestDesktopPairAddressAcceptanceRefusalsKeepTheOldAddress(t *testing.T) {
	f := newDesktopMCPFixture(t)
	f.redeem(t)
	original := desktopContact(t, f, f.bIdentity)
	other := startFullPeerFixture(t)
	for _, tc := range []struct {
		name, contact, destination, reason string
	}{
		{"invalid destination", f.bIdentity, "not-a-host-port", "destination must be HOST:PORT"},
		{"unknown contact", "not paired", f.b.destination, "not paired"},
		{"different workspace", f.bIdentity, other.destination, "identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := f.client.CallTool(ctx, &mcp.CallToolParams{Name: "pair_address", Arguments: map[string]any{
				"contact": tc.contact, "destination": tc.destination,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || !strings.Contains(strings.ToLower(desktopError(result)), strings.ToLower(tc.reason)) {
				t.Fatalf("%s: missing refusal %q: %s", tc.name, tc.reason, desktopError(result))
			}
			if got := desktopContact(t, f, f.bIdentity); got.Destination != original.Destination || got.IdentityKey != original.IdentityKey {
				t.Fatalf("refusal changed the contact: %+v", got)
			}
		})
	}
}

func desktopStatus(t *testing.T, f *desktopMCPFixture) desktop.StatusResult {
	t.Helper()
	var status desktop.StatusResult
	decodeDesktopMCP(t, desktopTool(t, f.client, "pair_status", map[string]any{}), &status)
	return status
}

func desktopContact(t *testing.T, f *desktopMCPFixture, identity string) protocol.PairingContactView {
	t.Helper()
	for _, contact := range desktopStatus(t, f).Contacts {
		if contact.IdentityKey == identity {
			return contact
		}
	}
	t.Fatalf("paired contact %s disappeared", identity)
	return protocol.PairingContactView{}
}

func containsDesktopHistory(messages []protocol.ContactMessageView, text string) bool {
	for _, message := range messages {
		if message.Text == text {
			return true
		}
	}
	return false
}

func sameJoinIDs(before, after []protocol.PairingJoinView) bool {
	if len(before) != len(after) {
		return false
	}
	for i := range before {
		if before[i].OperationID != after[i].OperationID {
			return false
		}
	}
	return true
}

func desktopError(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if item, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, item.Text)
		}
	}
	return strings.Join(parts, " ")
}

// alternateAddress exposes the same workspace on a second TCP address, as
// after its network changed. The original pairing and its database stay put.
func (f *fullPeerFixture) alternateAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); listener.Close() })
	pairSocket := filepath.Join(filepath.Dir(f.operatorSocket), "pair.sock")
	relay := peer.NewRelay(peer.RelayConfig{Dial: func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", pairSocket)
	}})
	go relay.Serve(ctx, listener)
	return listener.Addr().String()
}
