//go:build linux

package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPairedClaimsDesktopMCPAcceptanceRemoteClaimSurvivesToolSchema(t *testing.T) {
	f := newDesktopMCPFixture(t)
	f.redeem(t)
	worker := f.b.connectAgent(t, "b-worker")
	f.b.shareAgents(t, f.identity, "b-worker")
	sent := f.sendToAgent(t, "b-worker", "claim through Desktop MCP")
	got := awaitAgentMessage(t, worker, "claim through Desktop MCP")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := worker.Claim(ctx, protocol.Message{Type: protocol.TypeClaimSet,
		MessageID: got.MessageID, Task: "investigate", Role: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	var claim protocol.Claim
	awaitDesktop(t, "Desktop MCP inbox exposes the remote claim", func() bool {
		var found bool
		claim, found = desktopClaimFor(t, f.inbox(t).Conversations[0].Messages, sent.ID)
		return found
	})
	if claim.AgentID != "b-worker" {
		t.Fatalf("claim holder changed through MCP: %+v", claim)
	}
	if claim.Task != "investigate" {
		t.Fatalf("claim task changed through MCP: %+v", claim)
	}
	if claim.Role != "owner" {
		t.Fatalf("claim role changed through MCP: %+v", claim)
	}
	if claim.State != "active" {
		t.Fatalf("claim state changed through MCP: %+v", claim)
	}
	if claim.Remote == nil || claim.Remote.Workspace != f.bIdentity {
		t.Fatalf("remote claim lost canonical workspace attribution: %+v", claim)
	}
}

func desktopClaimFor(t *testing.T, messages []protocol.ContactMessageView, id string) (protocol.Claim, bool) {
	t.Helper()
	for _, message := range messages {
		if message.ID != id {
			continue
		}
		data, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		var page protocol.ClaimPage
		if err := json.Unmarshal(data, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Claims) == 1 {
			return page.Claims[0], true
		}
	}
	return protocol.Claim{}, false
}
