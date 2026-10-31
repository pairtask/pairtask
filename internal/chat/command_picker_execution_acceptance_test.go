package chat_test

import (
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
)

func commandChoose(t *testing.T, h *chatHarness, item string, key rune) {
	t.Helper()
	p := h.m.State().Picker
	if p == nil {
		t.Fatalf("no picker for %s", item)
	}
	index := slices.Index(p.Items, item)
	if index < 0 {
		t.Fatalf("%s missing from %v", item, p.Items)
	}
	for p.Selected != index {
		h.input(chatKey(tea.KeyDown, 0))
		p = h.m.State().Picker
	}
	h.input(chatKey(key, 0))
}

func TestCommandPickerExecutionAcceptanceAckAllRunsOnSelection(t *testing.T) {
	h := ackPickerHarness(t, true)
	v2Command(h, "/ack")
	chatEqual(t, "opening does not acknowledge", h.m.State().InFlight, 0)
	commandChoose(t, h, "all", tea.KeyEnter)
	req := h.take(protocol.TypeOperatorInboxStatus)
	chatEqual(t, "one status read", h.m.State().InFlight, 1)
	usage := bulkUsage(3)
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorInboxStatus, Inbox: &usage})
	ack := h.take(protocol.TypeOperatorAckMany)
	chatEqual(t, "all sender scope", ack.AckSelection.From, "")
	chatEqual(t, "observed message bound", ack.AckSelection.UpperSeq, uint64(3))
}

func TestCommandPickerExecutionAcceptanceReviewAgentThenPermission(t *testing.T) {
	for _, mode := range []string{"ask", "auto", "off"} {
		t.Run(mode, func(t *testing.T) {
			h, key := sharePickerHarness(t, "claude-1", "codex-1")
			v2Type(h, "keep draft")
			v2Command(h, "/pair reviews")
			commandChoose(t, h, "claude-1", tea.KeyEnter)
			chatEqual(t, "intermediate agent changes nothing", h.m.State().InFlight, 0)
			commandChoose(t, h, mode, tea.KeyEnter)
			req := h.take(protocol.TypeOperatorContact)
			chatEqual(t, "grant operation", req.ContactOp, protocol.ContactOpReviewGrant)
			chatEqual(t, "grant workspace", req.ContactKey, key)
			if !slices.Equal(req.ContactAgents, []string{"claude-1"}) {
				t.Fatalf("grant agents = %v", req.ContactAgents)
			}
			if req.ContactReviewGrant == nil {
				t.Fatal("grant missing")
			}
			chatEqual(t, "selected permission", req.ContactReviewGrant.Mode, mode)
			chatEqual(t, "no publication implied", req.ContactReviewGrant.Publish, false)
			chatEqual(t, "no formal approval implied", req.ContactReviewGrant.Formal, false)
			chatEqual(t, "message draft preserved", h.m.State().Draft, "keep draft")
		})
	}
}

func TestCommandPickerExecutionAcceptanceTabAllowsOptionalReviewFlags(t *testing.T) {
	h, _ := sharePickerHarness(t, "claude-1")
	v2Command(h, "/pair reviews")
	commandChoose(t, h, "claude-1", tea.KeyTab)
	commandChoose(t, h, "auto", tea.KeyTab)
	chatEqual(t, "Tab never changes grants", h.m.State().InFlight, 0)
	commandChoose(t, h, "--publish", tea.KeyEnter)
	req := h.take(protocol.TypeOperatorContact)
	if req.ContactReviewGrant == nil {
		t.Fatal("grant missing")
	}
	chatEqual(t, "explicit publication flag", req.ContactReviewGrant.Publish, true)
	chatEqual(t, "formal stays off", req.ContactReviewGrant.Formal, false)
}

func TestCommandPickerExecutionAcceptanceKnownMessageArgumentsAreOffered(t *testing.T) {
	for _, command := range []string{"stages", "claims", "copy"} {
		t.Run(command, func(t *testing.T) {
			h := recallHarness(t, recallSent("sent", 2, "alice"), protocol.HistoryEntry{Message: chatIncoming("received", 1), Direction: "received", OperatorStage: protocol.StageAccepted})
			v2Command(h, "/"+command)
			p := h.m.State().Picker
			if p == nil {
				t.Fatal("known message choices missing")
			}
			if !slices.Contains(p.Items, "#2") {
				t.Fatalf("sent message missing from %v", p.Items)
			}
			if command != "copy" && slices.Contains(p.Items, "#1") {
				t.Fatal("received message offered for sent-only command")
			}
			chatEqual(t, "opening only offers choices", h.m.State().InFlight, 0)
		})
	}
}
