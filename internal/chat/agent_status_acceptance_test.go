package chat_test

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func agentStatusSnapshot(revision uint64, statuses ...protocol.AgentStatus) protocol.Message {
	return protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{
		Watermark: protocol.Watermark{Epoch: "epoch", Revision: revision},
		Agents:    []protocol.AgentPresence{{AgentID: "alice", SessionID: "session-alice", ProtocolVersion: protocol.Version}}, AgentsTotal: 1,
		AgentStatus: statuses, AgentStatusTotal: len(statuses),
	}}
}

func agentStatusHarness(t *testing.T, status protocol.AgentStatus) *chatHarness {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 160, Height: 30})
	req := h.take(protocol.TypeOperatorSnapshot)
	h.reply(req, agentStatusSnapshot(1, status))
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h
}

func agentStatusDeclaration(state string) protocol.AgentStatus {
	return protocol.AgentStatus{AgentID: "alice", SessionID: "session-alice", State: state, Task: "#133", Step: "acceptance tests", DeclaredAt: time.Now().Add(-12 * time.Minute), Connected: true}
}

func TestAgentStatusChatAcceptanceShowsTaskWithoutInferringBlocked(t *testing.T) {
	h := agentStatusHarness(t, agentStatusDeclaration("active"))
	conversationOpen(h, "alice")
	frame := ansi.Strip(h.m.View().Content)
	for _, word := range []string{"#133", "active"} {
		v3Contains(t, frame, word)
	}
	v2Command(h, "/agents")
	details := ansi.Strip(h.m.View().Content)
	for _, word := range []string{"#133", "active", "acceptance tests", "unavailable"} {
		v3Contains(t, details, word)
	}
	if strings.Contains(details, "blocked") || strings.Contains(details, "stalled") {
		t.Fatalf("old activity inferred a state: %s", details)
	}
	chatEqual(t, "status is not a transcript message", len(h.m.State().Entries), 0)
	chatEqual(t, "status is not unread work", h.m.State().Unread, 0)
	for _, size := range [][2]int{{80, 24}, {40, 12}, {20, 6}} {
		h.input(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		v2FrameFits(t, h, size[0], size[1])
	}
}

func TestAgentStatusChatAcceptanceEventRefreshPreservesComposer(t *testing.T) {
	h := agentStatusHarness(t, agentStatusDeclaration("active"))
	conversationOpen(h, "alice")
	v2Type(h, "my unsent question")
	before := h.m.State()
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: "status_changed", AgentID: "alice", Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	req := h.take(protocol.TypeOperatorSnapshot)
	updated := agentStatusDeclaration("blocked")
	updated.Step = "needs review"
	updated.WaitingOn = "operator"
	updated.DeclaredAt = time.Now()
	h.reply(req, agentStatusSnapshot(2, updated))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	frame := ansi.Strip(h.m.View().Content)
	v3Contains(t, frame, "blocked")
	after := h.m.State()
	chatEqual(t, "draft kept", after.Draft, before.Draft)
	chatEqual(t, "conversation kept", after.Selected, before.Selected)
	chatEqual(t, "scroll kept", after.ScrollOffset, before.ScrollOffset)
	chatEqual(t, "unread unchanged", after.Unread, before.Unread)
	chatEqual(t, "no status transcript entry", len(after.Entries), len(before.Entries))
	chatEqual(t, "no status acknowledgment", len(after.QueuedAcks), 0)
	for _, request := range h.requests {
		switch request.Type {
		case protocol.TypeOperatorReceive, protocol.TypeOperatorHistory, protocol.TypeOperatorAck:
			t.Fatalf("status-only event caused messaging operation: %+v", request)
		}
	}
}

func TestAgentStatusChatAcceptanceSeparatesMilestoneAndActivityAges(t *testing.T) {
	status := agentStatusDeclaration("waiting")
	status.WaitingOn = "ci"
	status.Ref = "PR 136"
	status.Activity = &protocol.Activity{At: time.Now().Add(-2 * time.Minute), Source: "codex-launcher"}
	h := agentStatusHarness(t, status)
	v2Command(h, "/agents")
	panel := ansi.Strip(h.m.View().Content)
	for _, text := range []string{"#133", "waiting", "ci", "PR 136", "12m", "2m"} {
		v3Contains(t, panel, text)
	}
	if !strings.Contains(panel, "launcher") && !strings.Contains(panel, "codex") {
		t.Fatalf("activity source absent: %s", panel)
	}
}

func TestAgentStatusChatAcceptanceMarksPriorSessionDeclaration(t *testing.T) {
	status := agentStatusDeclaration("active")
	status.SessionID = "previous-session"
	status.Connected = false
	h := agentStatusHarness(t, status)
	v2Command(h, "/agents")
	panel := ansi.Strip(h.m.View().Content)
	v3Contains(t, panel, "#133")
	requireHistoricalStatus(t, panel)
}

func requireHistoricalStatus(t *testing.T, panel string) {
	t.Helper()
	for _, label := range []string{"offline", "previous", "historical", "old session", "old-session"} {
		if strings.Contains(panel, label) {
			return
		}
	}
	t.Fatalf("old declaration presented without session provenance: %s", panel)
}

func TestAgentStatusChatAcceptanceReconnectRecoversInterruptedRefresh(t *testing.T) {
	h := agentStatusHarness(t, agentStatusDeclaration("active"))
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: "status_changed", AgentID: "alice", Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	h.take(protocol.TypeOperatorSnapshot) // This response is lost with the transport.
	h.reconnectTo(newChatScript(), "epoch")
	baseline := h.take(protocol.TypeOperatorSnapshot)
	h.reply(baseline, agentStatusSnapshot(2, agentStatusDeclaration("waiting")))
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: "status_changed", AgentID: "alice", Watermark: protocol.Watermark{Epoch: "epoch", Revision: 3}}}
	refresh := h.take(protocol.TypeOperatorSnapshot)
	h.reply(refresh, agentStatusSnapshot(3, agentStatusDeclaration("done")))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	conversationOpen(h, "alice")
	v3Contains(t, ansi.Strip(h.m.View().Content), "done")
}

func TestAgentStatusChatAcceptancePresenceInvalidatesOldSession(t *testing.T) {
	for _, state := range []string{"disconnected", "connected"} {
		t.Run(state, func(t *testing.T) {
			h := agentStatusHarness(t, agentStatusDeclaration("active"))
			session := "session-alice"
			if state == "connected" {
				session = "new-session"
			}
			h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventPresenceChanged, AgentID: "alice", SessionID: session, State: state, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
			h.await(func() bool { return h.m.State().Revision == 2 })
			v2Command(h, "/agents")
			requireHistoricalStatus(t, ansi.Strip(h.m.View().Content))
		})
	}
}

func TestAgentStatusChatAcceptanceFooterShowsCompactStatus(t *testing.T) {
	h := agentStatusHarness(t, agentStatusDeclaration("active"))
	footer := ansi.Strip(footerRow(h))
	for _, text := range []string{"alice", "active", "12m"} {
		v3Contains(t, footer, text)
	}
	conversationOpen(h, "alice")
	footer = ansi.Strip(footerRow(h))
	for _, text := range []string{"alice", "active", "12m"} {
		v3Contains(t, footer, text)
	}
	h.input(tea.WindowSizeMsg{Width: 80, Height: 24})
	v2Command(h, "/ack #999")
	h.input(chatKey(tea.KeyEscape, 0))
	v3Contains(t, ansi.Strip(footerRow(h)), h.m.State().Status)
	v2FrameFits(t, h, 80, 24)
}

func TestAgentStatusChatAcceptanceFooterKeepsAckWarningPriority(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 80, Height: 24})
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	response := agentStatusSnapshot(1, agentStatusDeclaration("active"))
	response.Snapshot.InboxHeadSeq, response.Snapshot.InboxPending = 12, 12
	h.reply(snapshot, response)
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	request := h.take(protocol.TypeOperatorInboxStatus)
	usage := bulkUsage(12)
	usage.HeadSeq = 12
	h.reply(request, protocol.Message{Type: protocol.TypeOperatorInboxStatus, Inbox: &usage})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	for _, width := range []int{80, 160} {
		h.input(tea.WindowSizeMsg{Width: width, Height: 24})
		footer := ansi.Strip(footerRow(h))
		for _, text := range []string{"12 need ack", "/ack all", "12/16"} {
			v3Contains(t, footer, text)
		}
		if width == 160 {
			v3Contains(t, footer, "alice")
			v3Contains(t, footer, "active")
		}
		v2FrameFits(t, h, width, 24)
	}
	chatEqual(t, "status footer cannot acknowledge", len(h.m.State().QueuedAcks), 0)
}

func TestAgentStatusChatAcceptanceSnapshotAdvanceStillReconcilesMessages(t *testing.T) {
	h := agentStatusHarness(t, agentStatusDeclaration("active"))
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: "status_changed", AgentID: "alice", Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	req := h.take(protocol.TypeOperatorSnapshot)
	// The broker cut the snapshot after another event that the chat never
	// received. It must recover messaging state, even for a status refresh.
	response := agentStatusSnapshot(3, agentStatusDeclaration("waiting"))
	response.Snapshot.InboxHeadSeq = 7
	response.Snapshot.InboxPending = 1
	h.reply(req, response)
	inbox := h.take(protocol.TypeOperatorReceive)
	history := h.take(protocol.TypeOperatorHistory)
	if inbox.RequestID == "" || history.RequestID == "" {
		t.Fatal("advanced snapshot did not reconcile messaging")
	}
}
