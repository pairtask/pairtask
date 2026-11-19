//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func agentsCall(t *testing.T, socket string, flags ...string) (int, string, string) {
	t.Helper()
	args := append([]string{"agents", "--socket", socket}, flags...)
	var out, errout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code := runOperator(ctx, args, &out, &errout)
	return code, out.String(), errout.String()
}

func agentsFixture(t *testing.T) (*offlineInboxFixture, *offlineWire) {
	t.Helper()
	f := newOfflineInbox(t)
	summarySeed(t, f, 3) // bob1, alice2, bob3; every message is still unreceived.
	f.adopt(t, "summary-2", protocol.Recipient{AgentID: "operator", SessionID: "operator-session"}, "")
	f.seed(t, offlineSeed{id: "outgoing-not-agent-text", operator: true, targets: []string{"reader"}, pending: "reader"})
	offlineOK(t, f.db.SetAgentStatus(context.Background(), protocol.AgentStatus{
		AgentID: "bob", SessionID: "old-bob-session", State: "blocked", Task: "reviewing", Step: "waiting for CI", DeclaredAt: time.Now().UTC(),
	}))
	w := summaryStart(t, f)
	p := offlineDial(t, w.agent)
	welcome := p.exchange(t, protocol.Message{Type: protocol.TypeHello, AgentID: "alice", ProtocolVersion: protocol.Version})
	if welcome.Type != protocol.TypeWelcome {
		t.Fatalf("hello failed: %+v", welcome)
	}
	result := p.exchange(t, protocol.Message{Type: protocol.TypeAgentStatusSet, RequestID: "declare-alice", AgentStatus: &protocol.AgentStatus{State: "active", Task: "building", Step: "running checks"}})
	if result.Type != protocol.TypeAgentStatusResult {
		t.Fatalf("status failed: %+v", result)
	}
	return f, w
}

func TestOperatorAgentsAcceptanceShowsWorkAndEachAgentsLastMessageWithoutConsuming(t *testing.T) {
	f, w := agentsFixture(t)
	before := offlineReceipts(t, f)
	code, out, stderr := agentsCall(t, w.operator)
	if code != 0 {
		t.Fatalf("agents exit%d: %s", code, stderr)
	}
	for _, word := range []string{"alice", "active", "building", "text-2", "bob", "blocked", "reviewing", "text-3"} {
		if !strings.Contains(out, word) {
			t.Fatalf("missing %q: %q", word, out)
		}
	}
	if strings.Contains(out, "text-1") || strings.Contains(out, "outgoing-not-agent-text") {
		t.Fatalf("wrong last message: %q", out)
	}
	if strings.ContainsAny(out, "\x1b\r\x00") || strings.Contains(out, "second-line") {
		t.Fatalf("unsafe last-message preview: %q", out)
	}
	if before != offlineReceipts(t, f) {
		t.Fatal("agents changed inbox receipt state")
	}
}

func TestOperatorAgentsAcceptanceJSONContainsCurrentAndDisconnectedRecords(t *testing.T) {
	f, w := agentsFixture(t)
	before := offlineReceipts(t, f)
	code, out, stderr := agentsCall(t, w.operator, "--json")
	if code != 0 || !json.Valid([]byte(out)) {
		t.Fatalf("bad agents JSON exit%d: %q %s", code, out, stderr)
	}
	for _, word := range []string{"alice", "bob", "building", "reviewing", "text-2", "text-3"} {
		if !strings.Contains(out, word) {
			t.Fatalf("missing %q: %s", word, out)
		}
	}
	if before != offlineReceipts(t, f) {
		t.Fatal("JSON agents changed inbox receipt state")
	}
}

func TestOperatorAgentsAcceptanceUnavailableBrokerIsNotReportedAsAnEmptyRoster(t *testing.T) {
	code, out, stderr := agentsCall(t, "/does/not/exist")
	if code == 0 || out != "" || stderr == "" {
		t.Fatalf("unavailable broker hidden: exit%d %q %q", code, out, stderr)
	}
}

func TestOperatorAgentsAcceptanceWorksWhileChatOwnsOperatorSession(t *testing.T) {
	f, w := agentsFixture(t)
	owner := offlineDial(t, w.operator)
	opened := owner.exchange(t, protocol.Message{Type: protocol.TypeOperatorSessionOpen, RequestID: "open-chat", ProtocolVersion: protocol.Version})
	if opened.Type != protocol.TypeOperatorSession {
		t.Fatalf("open chat: %s %s", opened.Code, opened.Detail)
	}
	before := offlineReceipts(t, f)
	code, out, stderr := agentsCall(t, w.operator)
	if code != 0 || !strings.Contains(out, "alice") {
		t.Fatalf("agents interfered with chat: exit%d %q %s", code, out, stderr)
	}
	snapshot := owner.exchange(t, protocol.Message{Type: protocol.TypeOperatorSnapshot, RequestID: "still-owner"})
	if snapshot.Type != protocol.TypeOperatorSnapshot {
		t.Fatalf("chat lost ownership: %s %s", snapshot.Code, snapshot.Detail)
	}
	if before != offlineReceipts(t, f) {
		t.Fatal("agents consumed chat inbox")
	}
}
