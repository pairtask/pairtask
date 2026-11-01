package chat_test

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func assignmentChatHarness(t *testing.T, supported bool) *chatHarness {
	t.Helper()
	s := newChatScript()
	h := &chatHarness{t: t, wire: s, results: make(chan tea.Msg, 128), done: make(chan struct{}), dials: make(chan chatDialResult, 8)}
	caps := slices.Clone(protocol.SessionCapabilities)
	if !supported {
		caps = slices.DeleteFunc(caps, func(cap string) bool { return cap == "assignments" })
	}
	h.dials <- chatDialResult{wire: s, session: protocol.SessionInfo{SessionID: "owner", Epoch: "epoch", Capabilities: caps, OpenedAt: time.Now()}}
	h.m = chat.New(chat.Options{Dial: func(context.Context) (chat.Transport, protocol.SessionInfo, error) {
		select {
		case result := <-h.dials:
			return result.wire, result.session, result.err
		case <-h.done:
			return nil, protocol.SessionInfo{}, context.Canceled
		}
	}})
	t.Cleanup(func() { close(h.done); s.Close() })
	h.launch(h.m.Init())
	h.input(tea.WindowSizeMsg{Width: 160, Height: 40})
	h.reply(h.take(protocol.TypeOperatorSnapshot), protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{
		Watermark: protocol.Watermark{Epoch: "epoch", Revision: 1},
		Agents:    []protocol.AgentPresence{{AgentID: "alice", ProtocolVersion: 16}, {AgentID: "bob", ProtocolVersion: 16}}, AgentsTotal: 2,
	}})
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h
}

func TestAssignmentsChatAcceptanceSquareTagIsPublicWithCanonicalMentions(t *testing.T) {
	h := assignmentChatHarness(t, true)
	v2Type(h, "@bob @alice @alice answer here")
	sent := v2Send(h)
	chatEqual(t, "tag stays in Square", sent.To, "*")
	chatEqual(t, "Square has no room", sent.Room, "")
	chatEqual(t, "tag never becomes private group", len(sent.Group), 0)
	if !slices.Equal(sent.Mentions, []string{"alice", "bob"}) {
		t.Fatalf("canonical public tags lost: %+v", sent)
	}
	chatEqual(t, "draft body retained", v2Body(t, sent), "@bob @alice @alice answer here")

	next := newChatScript()
	h.reconnectTo(next, "epoch")
	h.reply(h.take(protocol.TypeOperatorSnapshot), protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}})
	retry := h.take(protocol.TypeOperatorSend)
	sent.RequestID, retry.RequestID = "", ""
	if !reflect.DeepEqual(sent, retry) {
		t.Fatalf("retry changed frozen public audience or assignments: %+v => %+v", sent, retry)
	}
}

func TestAssignmentsChatAcceptanceOldBrokerRefusesTagAndPreservesDraft(t *testing.T) {
	h := assignmentChatHarness(t, false)
	draft := "@alice answer in public"
	v2Type(h, draft)
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "unsupported tag has no outgoing intent", h.m.State().Intent, nil)
	chatEqual(t, "unsupported tag sends nothing", h.m.State().InFlight, 0)
	chatEqual(t, "unsupported draft retained", h.m.State().Draft, draft)
	notice := strings.ToLower(h.m.State().Hint + " " + h.m.State().Status)
	for _, want := range []string{"upgrade", "public"} {
		v3Contains(t, notice, want)
	}
}

func TestAssignmentsChatAcceptanceExplicitAgentTabRemainsPrivate(t *testing.T) {
	for _, supported := range []bool{false, true} {
		h := assignmentChatHarness(t, supported)
		conversationOpen(h, "alice")
		v2Type(h, "@alice private answer")
		sent := v2Send(h)
		chatEqual(t, "agent tab remains explicit private route", sent.To, "alice")
		chatEqual(t, "private send creates no assignment tags", len(sent.Mentions), 0)
	}
}

func assignmentChatSnapshot(t *testing.T, revision int, states ...string) protocol.Message {
	t.Helper()
	var assignments []map[string]any
	for i, state := range states {
		record := map[string]any{"operator_message_id": "task-parent", "agent_id": []string{"alpha", "beta", "gamma"}[i], "task": "reply", "state": state, "assigned_at": "2026-11-01T00:00:00Z"}
		if state != "assigned" {
			record["finished_at"] = "2026-11-01T01:00:00Z"
		}
		assignments = append(assignments, record)
	}
	encoded, err := json.Marshal(map[string]any{
		"watermark": map[string]any{"epoch": "epoch", "revision": revision},
		"agents":    []map[string]any{{"agent_id": "alpha", "protocol_version": 16}, {"agent_id": "beta", "protocol_version": 16}, {"agent_id": "gamma", "protocol_version": 16}}, "agents_total": 3,
		"claims": []map[string]any{{"operator_message_id": "task-parent", "claims": []any{}, "claims_total": 0, "claims_truncated": false,
			"assignments": assignments, "assignments_total": len(assignments), "assignments_truncated": false}}, "claims_total": 1, "claims_truncated": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	var snapshot protocol.OperatorSnapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		t.Fatal(err)
	}
	return protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &snapshot}
}

func TestAssignmentsChatAcceptanceTimelineClaimsAgentsAndAllReleasedRefresh(t *testing.T) {
	h := claimChatHarness(t, assignmentChatSnapshot(t, 1, "assigned", "done", "released"), "task-parent", 7)
	h.input(tea.WindowSizeMsg{Width: 160, Height: 50})
	for _, want := range []string{"reply", "alpha", "assigned", "beta", "done", "gamma", "released"} {
		v3Contains(t, ansi.Strip(h.m.View().Content), want)
	}
	v2Command(h, "/claims #7")
	for _, want := range []string{"alpha", "assigned", "beta", "done", "gamma", "released"} {
		v3Contains(t, ansi.Strip(h.m.View().Content), want)
	}
	h.input(chatKey(tea.KeyEscape, 0))
	v2Command(h, "/agents")
	for _, want := range []string{"alpha", "assigned", "beta", "done", "reply"} {
		v3Contains(t, ansi.Strip(h.m.View().Content), want)
	}
	h.input(chatKey(tea.KeyEscape, 0))
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: "claims_changed", OperatorMessageID: "task-parent", Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	h.reply(h.take(protocol.TypeOperatorSnapshot), assignmentChatSnapshot(t, 2, "released", "released", "released"))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, ansi.Strip(h.m.View().Content), "nobody holds reply: all released")
	v2Command(h, "/claims #7")
	v3Contains(t, ansi.Strip(h.m.View().Content), "nobody holds reply: all released")
	chatEqual(t, "assignment changes never invent messages", len(h.m.State().Entries), 1)
	chatEqual(t, "assignment changes never acknowledge messages", len(h.m.State().QueuedAcks), 0)
	for _, width := range []int{32, 60, 120} {
		h.input(tea.WindowSizeMsg{Width: width, Height: 40})
		v2FrameFits(t, h, width, 40)
	}
}
