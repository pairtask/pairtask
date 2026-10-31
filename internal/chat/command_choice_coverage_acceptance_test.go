package chat_test

import (
	"encoding/json"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
)

func commandContactChoices(t *testing.T) (*chatHarness, string) {
	t.Helper()
	h, key := sharePickerHarness(t, "local-agent")
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
		Kind: protocol.EventContactMessagesChanged, ContactKey: key,
		Watermark: protocol.Watermark{Epoch: "epoch", Revision: h.m.State().Revision + 1},
	}}
	req := h.take(protocol.TypeOperatorContact)
	page := contactMessagesPage(key)
	page.ContactPage.Shared = []string{"local-agent"}
	page.ContactPage.Directory = []string{"remote-agent"}
	page.ContactPage.Messages = []protocol.ContactMessageView{{ID: "pending-review", Contact: key, Direction: "in", Seq: 1, ReviewState: "awaiting", ReviewAsk: &protocol.ReviewAsk{}}}
	h.reply(req, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h, key
}

func TestCommandChoiceCoverageAcceptanceContactVerbsOfferKnownValues(t *testing.T) {
	for _, tc := range []struct{ line, choice string }{
		{"pair unshare", "local-agent"},
		{"pair review", "remote-agent"},
		{"pair approve", "pending-review"},
		{"pair decline", "pending-review"},
		{"pair name", "--clear"},
	} {
		t.Run(tc.line, func(t *testing.T) {
			h, _ := commandContactChoices(t)
			v2Command(h, "/"+tc.line)
			p := h.m.State().Picker
			if p == nil {
				t.Fatal("known choices missing")
			}
			if !slices.Contains(p.Items, tc.choice) {
				t.Fatalf("choice %q absent from %v", tc.choice, p.Items)
			}
			chatEqual(t, "opening choices sends nothing", h.m.State().InFlight, 0)
		})
	}
}

func TestCommandChoiceCoverageAcceptanceDiagramOffersMessagesAndBlocks(t *testing.T) {
	diagram := chatIncoming("diagram", 1)
	diagram.Payload, _ = json.Marshal(map[string]string{"text": "```mermaid\nflowchart LR\n A --> B\n```\n```plantuml\n@startuml\nA -> B\n@enduml\n```"})
	h := recallHarness(t,
		protocol.HistoryEntry{Message: chatIncoming("plain", 2), Direction: "received", OperatorStage: protocol.StageAccepted},
		protocol.HistoryEntry{Message: diagram, Direction: "received", OperatorStage: protocol.StageAccepted})
	v2Command(h, "/diagram")
	commandCompleteItems(t, h, []string{"#1"})
	commandChoose(t, h, "#1", tea.KeyEnter)
	commandCompleteItems(t, h, []string{"1", "2"})
	chatEqual(t, "multiple diagrams need a block choice", h.m.State().CommandLine != nil, true)
}

func TestCommandChoiceCoverageAcceptanceCopyRefOffersAgents(t *testing.T) {
	h := commandCompleteHarness(t)
	v2Command(h, "/copy")
	commandChoose(t, h, "ref", tea.KeyEnter)
	chatEqual(t, "ref still needs agent", h.m.State().CommandLine != nil, true)
	p := h.m.State().Picker
	if p == nil {
		t.Fatal("reference agents missing")
	}
	if !slices.Contains(p.Items, "alice") {
		t.Fatalf("alice absent from %v", p.Items)
	}
	chatEqual(t, "intermediate choice sends nothing", h.m.State().InFlight, 0)
}

func TestCommandChoiceCoverageAcceptanceReviewDecisionRunsSelectedRequest(t *testing.T) {
	for _, verb := range []string{"approve", "decline"} {
		t.Run(verb, func(t *testing.T) {
			h, key := commandContactChoices(t)
			v2Command(h, "/pair "+verb)
			commandChoose(t, h, "pending-review", tea.KeyEnter)
			req := h.take(protocol.TypeOperatorContact)
			chatEqual(t, "decision operation", req.ContactOp, protocol.ContactOpReviewDecide)
			chatEqual(t, "decision workspace", req.ContactKey, key)
			if !slices.Equal(req.ContactRead, []string{"pending-review"}) {
				t.Fatalf("decision requests = %v", req.ContactRead)
			}
			if req.ContactApprove == nil {
				t.Fatal("decision missing")
			}
			chatEqual(t, "selected decision", *req.ContactApprove, verb == "approve")
		})
	}
}
