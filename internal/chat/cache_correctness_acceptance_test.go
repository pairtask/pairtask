package chat

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func cacheAcceptanceEntry(t *testing.T) (*Model, *entry) {
	t.Helper()
	m := performanceModel(t, 1)
	m.view.following = true
	e := m.timeline.order[0]
	e.msg.Payload = json.RawMessage(`{"text":"cache marker"}`)
	e.hash = bodyHash(e.msg.Payload)
	return m, e
}

func cacheAcceptanceText(m *Model) string { return ansi.Strip(m.View().Content) }

func TestChatCacheAcceptanceReceivedMetadataRefreshes(t *testing.T) {
	cases := []struct {
		name, want string
		change     func(*Model, *entry)
	}{
		{"ack-confirmed", AckDone, func(_ *Model, e *entry) { e.operatorStage = protocol.StageAgentAcknowledged }},
		{"ack-queued", AckQueued, func(m *Model, e *entry) { m.inbox.queuedAcks[e.msg.MessageID] = &queuedAck{} }},
		{"receipt-failure", "receipt failed", func(_ *Model, e *entry) { e.receipt = ReceiptFailed }},
		{"conflict", "CONFLICT", func(_ *Model, e *entry) { e.conflict = true }},
		{"history-gap", "older messages may be missing", func(_ *Model, e *entry) { e.gapBelow = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, e := cacheAcceptanceEntry(t)
			_ = m.View()
			tc.change(m, e)
			m.Update(statusTick{})
			if got := cacheAcceptanceText(m); !strings.Contains(got, tc.want) {
				t.Fatalf("updated metadata missing %q:\n%s", tc.want, got)
			}
		})
	}
}

func TestChatCacheAcceptanceQueuedAckReasonRefreshes(t *testing.T) {
	m, e := cacheAcceptanceEntry(t)
	q := &queuedAck{}
	m.inbox.queuedAcks[e.msg.MessageID] = q
	_ = m.View()
	q.suspended = "disconnected"
	m.Update(statusTick{})
	if got := cacheAcceptanceText(m); !strings.Contains(got, "suspended: disconnected") {
		t.Fatalf("stale queued acknowledgment:\n%s", got)
	}
	delete(m.inbox.queuedAcks, e.msg.MessageID)
	m.Update(statusTick{})
	if got := cacheAcceptanceText(m); strings.Contains(got, "suspended:") || !strings.Contains(got, AckNeeded) {
		t.Fatalf("removed acknowledgment remained visible:\n%s", got)
	}
}

func TestChatCacheAcceptanceSentClaimsAndStagesRefresh(t *testing.T) {
	m, e := cacheAcceptanceEntry(t)
	e.direction = "sent"
	_ = m.View()
	m.claims.set(&protocol.OperatorSnapshot{Claims: []protocol.ClaimSet{{OperatorMessageID: e.msg.MessageID, ClaimPage: protocol.ClaimPage{Claims: []protocol.Claim{{AgentID: "reviewer", Task: "cache-review", Role: protocol.RoleOwner, State: protocol.ClaimActive}}}}}})
	e.applyStage(protocol.RecipientStage{AgentID: "reviewer", Stage: protocol.StageAgentAcknowledged}, protocol.Watermark{Epoch: "e", Revision: 1})
	m.Update(statusTick{})
	got := cacheAcceptanceText(m)
	for _, want := range []string{"cache-review", "reviewer", "acknowledged"} {
		if !strings.Contains(got, want) {
			t.Fatalf("updated sent row missing %q:\n%s", want, got)
		}
	}
	m.claims.set(&protocol.OperatorSnapshot{})
	m.timeline.resetStages()
	m.Update(statusTick{})
	got = cacheAcceptanceText(m)
	if strings.Contains(got, "cache-review") || strings.Contains(got, "acknowledged") {
		t.Fatalf("removed claim/stage still cached:\n%s", got)
	}
}

func TestChatCacheAcceptanceOmittedBodyAndReplyResolve(t *testing.T) {
	m, e := cacheAcceptanceEntry(t)
	e.omitted = true
	e.msg.InReplyTo = "parent"
	_ = m.View()
	fresh := newEntry(e.msg, e.direction, false)
	m.timeline.upsert(fresh)
	parent := newEntry(protocol.Message{MessageID: "parent", Seq: 0, From: "operator", To: "agent", Payload: json.RawMessage(`{"text":"parent message"}`)}, "sent", false)
	m.timeline.insert(parent)
	m.Update(statusTick{})
	got := cacheAcceptanceText(m)
	if !strings.Contains(got, "cache marker") || strings.Contains(got, "text not available") || strings.Contains(got, `re "parent" (claim)`) {
		t.Fatalf("body or parent resolution stale:\n%s", got)
	}
	if !strings.Contains(got, "re #0") {
		t.Fatalf("loaded parent sequence not used:\n%s", got)
	}
}

func TestChatCacheAcceptanceScrollAnchorAndResize(t *testing.T) {
	m := performanceModel(t, 100)
	_ = m.View()
	before := m.transcriptWindow(m.layout().transcript)
	anchor := m.view
	msg := protocol.Message{MessageID: "late", Seq: 101, From: "agent", To: "operator", Payload: json.RawMessage(`{"text":"late arrival"}`)}
	m.timeline.insert(newEntry(msg, "received", false))
	m.Update(statusTick{})
	after := m.transcriptWindow(m.layout().transcript)
	if strings.Join(before, "\n") != strings.Join(after, "\n") || m.view != anchor {
		t.Fatal("arrival moved cached scroll anchor")
	}
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 36})
	if m.view.anchorID != anchor.anchorID || m.view.anchorSrc != anchor.anchorSrc || m.view.anchorCol != anchor.anchorCol {
		t.Fatal("resize lost logical anchor")
	}
	for _, row := range strings.Split(cacheAcceptanceText(m), "\n") {
		if ansi.StringWidth(row) > 60 {
			t.Fatalf("stale wide row after resize: %q", row)
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 36})
	restored := m.transcriptWindow(m.layout().transcript)
	if strings.Join(before, "\n") != strings.Join(restored, "\n") {
		t.Fatal("resize roundtrip changed anchored text")
	}
}

func TestChatCacheAcceptanceWarmInteractionAllocationBudget(t *testing.T) {
	m := performanceModel(t, 1000)
	_ = m.View()
	// This deliberately broad allocation budget catches rebuilding thousands
	// of markdown bodies without relying on scheduler-sensitive timing limits.
	allocations := testing.AllocsPerRun(3, func() {
		m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, Y: 5})
		_ = m.View()
		m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, Y: 5})
		_ = m.View()
	})
	if allocations > 100000 {
		t.Fatalf("warm up/down interaction allocates %.0f objects; budget 100000", allocations)
	}
}
