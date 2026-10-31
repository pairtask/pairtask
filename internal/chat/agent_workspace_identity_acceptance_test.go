package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func workspaceIdentityHarness(t *testing.T, c protocol.PairingContactView, rows ...protocol.ContactMessageView) *chatHarness {
	t.Helper()
	h := contactChatHarness(t, c)
	h.input(tea.WindowSizeMsg{Width: 200, Height: 40})
	h.input(chatKey(tea.KeyTab, 0))
	req := h.take(protocol.TypeOperatorContact)
	h.reply(req, contactMessagesPage(c.IdentityKey, rows...))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h
}

func TestAgentWorkspaceIdentityAcceptanceSidesAreExplicit(t *testing.T) {
	c := contactChatRow("sha256:"+strings.Repeat("a", 64), "Office", true)
	for _, tc := range []struct {
		direction, want string
		acks            []string
	}{
		{"in", "claude-1@Office → codex-1", nil},
		{"out", "claude-1 → codex-1@Office, reviewer@Office", []string{"codex-1"}},
	} {
		t.Run(tc.direction, func(t *testing.T) {
			to := []string{"codex-1"}
			if tc.direction == "out" {
				to = append(to, "reviewer")
			}
			h := workspaceIdentityHarness(t, c, protocol.ContactMessageView{ID: "identity", Cursor: 1, Direction: tc.direction, State: "read", From: "claude-1", To: to, AgentAcks: tc.acks, Text: "body"})
			view := ansi.Strip(h.m.View().Content)
			v3Contains(t, view, tc.want)
			if len(tc.acks) > 0 {
				v3Contains(t, view, "acknowledged by codex-1@Office")
			}
			chatEqual(t, "display sends nothing", h.m.State().InFlight, 0)
		})
	}
}

func TestAgentWorkspaceIdentityAcceptanceNameFallback(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct{ name, local, want string }{
		{"Peer name", "My office", "My office"},
		{"Peer name", "", "Peer name"},
		{"", "", protocol.ShortKey(key)},
	} {
		t.Run(tc.want, func(t *testing.T) {
			c := contactChatRow(key, tc.name, true)
			c.LocalName, c.Destination, c.Dials = tc.local, "192.0.2.14:7445", true
			h := workspaceIdentityHarness(t, c, protocol.ContactMessageView{ID: "fallback", Cursor: 1, Direction: "in", State: "read", From: "agent", Text: "body"})
			v3Contains(t, ansi.Strip(h.m.View().Content), "agent@"+tc.want)
		})
	}
}

func TestAgentWorkspaceIdentityAcceptanceBoundsAndEscapesNames(t *testing.T) {
	c := contactChatRow("sha256:"+strings.Repeat("b", 64), strings.Repeat("界", 20)+"\x1b[2J\x07", true)
	h := workspaceIdentityHarness(t, c, protocol.ContactMessageView{ID: "safe", Cursor: 1, Direction: "out", State: "read", From: "local", To: []string{"remote\x1b[2J\x07"}, Text: "body"})
	raw := h.m.View().Content
	for _, control := range []string{"\x1b[2J", "\x07"} {
		if strings.Contains(raw, control) {
			t.Fatalf("control reached terminal: %q", control)
		}
	}
	view := ansi.Strip(raw)
	start := strings.Index(view, "@界")
	if start < 0 {
		t.Fatalf("workspace suffix missing: %s", view)
	}
	suffix := strings.Fields(view[start+1:])[0]
	if !strings.HasSuffix(suffix, "…") {
		t.Fatalf("truncation not disclosed: %q", suffix)
	}
	if ansi.StringWidth(suffix) > 24 {
		t.Fatalf("workspace label exceeds 24 cells: %q", suffix)
	}
}

func TestAgentWorkspaceIdentityAcceptanceRenameUpdatesExistingMessage(t *testing.T) {
	c := contactChatRow("sha256:"+strings.Repeat("c", 64), "Old office", true)
	h := workspaceIdentityHarness(t, c, protocol.ContactMessageView{ID: "rename", Cursor: 1, Direction: "in", State: "read", From: "agent", Text: "body"})
	v2Command(h, "/pair name New office")
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "rename retains identity", req.ContactKey, c.IdentityKey)
	c.LocalName = "New office"
	resp := contactChatReply(c)
	resp.Type = protocol.TypeOperatorContactResult
	h.reply(req, resp)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "agent@New office")
	if strings.Contains(view, "agent@Old office") {
		t.Fatal("message retained stale workspace label")
	}
}
