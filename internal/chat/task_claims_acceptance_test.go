package chat_test

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func claimChatSnapshot(t *testing.T, revision int, state string) protocol.Message {
	t.Helper()
	data, err := json.Marshal(map[string]any{"watermark": map[string]any{"epoch": "epoch", "revision": revision}, "claims": []any{map[string]any{
		"operator_message_id": "task-parent", "claims_total": 2, "claims_truncated": false,
		"claims": []any{
			map[string]any{"operator_message_id": "task-parent", "task": "reply", "topic": "Answer the request", "role": "owner", "agent_id": "alpha", "state": state, "claimed_at": "2026-10-25T00:00:00Z"},
			map[string]any{"operator_message_id": "task-parent", "task": "reply", "topic": "Review the answer", "role": "reviewer", "agent_id": "beta", "state": "active", "claimed_at": "2026-10-25T00:00:00Z"},
		},
	}}, "claims_total": 1, "claims_truncated": false})
	if err != nil {
		t.Fatal(err)
	}
	var snapshot protocol.OperatorSnapshot
	if err = json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	return protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &snapshot}
}

func claimChatHarness(t *testing.T, snapshot protocol.Message, parentID string, seq uint64) *chatHarness {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 120, Height: 35})
	h.reply(h.take(protocol.TypeOperatorSnapshot), snapshot)
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	h.reply(inbox, chatInbox())
	parent := chatIncoming(parentID, seq)
	parent.From, parent.To, parent.Origin = protocol.OperatorID, protocol.Broadcast, protocol.OriginOperator
	page := chatHistory(parent)
	page.History.Entries[0].Direction = "sent"
	h.reply(history, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h
}

func TestTaskClaimsChatAcceptanceShowsOwnersUnderRequestAndRefreshes(t *testing.T) {
	h := claimChatHarness(t, claimChatSnapshot(t, 1, "active"), "task-parent", 7)
	frame := ansi.Strip(h.m.View().Content)
	for _, want := range []string{"reply", "alpha", "owner", "beta", "reviewer", "active"} {
		v3Contains(t, frame, want)
	}
	chatEqual(t, "claims do not invent messages", len(h.m.State().Entries), 1)
	chatEqual(t, "claims do not acknowledge messages", len(h.m.State().QueuedAcks), 0)
	v2Command(h, "/claims #7")
	panel := ansi.Strip(h.m.View().Content)
	for _, want := range []string{"reply", "alpha", "owner", "beta", "reviewer"} {
		v3Contains(t, panel, want)
	}
	h.input(chatKey(tea.KeyEscape, 0))
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: "claims_changed", OperatorMessageID: "task-parent", Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	h.reply(h.take(protocol.TypeOperatorSnapshot), claimChatSnapshot(t, 2, "done"))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	frame = ansi.Strip(h.m.View().Content)
	v3Contains(t, frame, "done")
	v3Contains(t, frame, "alpha")
	chatEqual(t, "updated claims still not messages", len(h.m.State().Entries), 1)
	for _, size := range [][2]int{{32, 12}, {52, 20}, {80, 24}} {
		h.input(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		v2FrameFits(t, h, size[0], size[1])
	}
}

func TestTaskClaimsChatAcceptanceMissingSnapshotEntryDoesNotInventApproval(t *testing.T) {
	snapshot := claimChatSnapshot(t, 1, "active")
	snapshot.Snapshot.ClaimsTotal = 33
	snapshot.Snapshot.ClaimsTruncated = true
	h := claimChatHarness(t, snapshot, "older-parent", 3)
	v2Command(h, "/claims #3")
	plain := strings.ToLower(ansi.Strip(h.m.View().Content))
	requirePartialClaimNotice(t, plain, []string{"truncat", "partial", "not loaded"})
	chatEqual(t, "read-only claims panel never sends", h.m.State().Intent, nil)
}

func TestTaskClaimsChatAcceptanceReportsTruncatedClaimHistoryForLoadedParent(t *testing.T) {
	snapshot := claimChatSnapshot(t, 1, "active")
	snapshot.Snapshot.Claims[0].Total = 20
	snapshot.Snapshot.Claims[0].Truncated = true
	h := claimChatHarness(t, snapshot, "task-parent", 7)
	v2Command(h, "/claims #7")
	plain := strings.ToLower(ansi.Strip(h.m.View().Content))
	requirePartialClaimNotice(t, plain, []string{"truncat", "partial", "of 20"})
	v3Contains(t, plain, "alpha")
}

func requirePartialClaimNotice(t *testing.T, plain string, notices []string) {
	t.Helper()
	for _, notice := range notices {
		if strings.Contains(plain, notice) {
			return
		}
	}
	t.Fatalf("bounded claim data was presented as complete: %s", plain)
}

func TestTaskClaimsChatAcceptanceWrappedIdentityIncludesIndentWidth(t *testing.T) {
	snapshot := claimChatSnapshot(t, 1, "active")
	claims := &snapshot.Snapshot.Claims[0]
	claims.Claims = claims.Claims[:1]
	claims.Total = 1
	name := strings.Repeat("a", 72) + "END"
	claims.Claims[0].AgentID = name
	h := claimChatHarness(t, snapshot, "task-parent", 7)
	for _, width := range []int{32, 80} {
		h.input(tea.WindowSizeMsg{Width: width, Height: 35})
		plain := strings.Join(strings.Fields(ansi.Strip(h.m.View().Content)), "")
		want := "reply:" + name + "(owner,active)"
		if !strings.Contains(plain, want) {
			t.Fatalf("width %d clipped a claim identity or state after adding transcript indent: %s", width, plain)
		}
	}
}
