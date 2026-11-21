//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Bounds of `operator agents`: a roster or a history too long to read in
// one call is shown as far as it goes and says so; it is never an error
// and never an unbounded walk.

type agentsBoundsView struct {
	Agents []struct {
		ID          string           `json:"id"`
		LastMessage *json.RawMessage `json:"last_message"`
	} `json:"agents"`
	LastMessagesTruncated bool `json:"last_messages_truncated"`
	RosterTruncated       bool `json:"roster_truncated"`
}

func agentsBoundsDeclare(t *testing.T, f *offlineInboxFixture, id string) {
	t.Helper()
	offlineOK(t, f.db.SetAgentStatus(context.Background(), protocol.AgentStatus{
		AgentID: id, SessionID: "session-" + id, State: "active", Task: "task of " + id, DeclaredAt: time.Now().UTC(),
	}))
}

// agentsBoundsHistory records count messages from agent-0..agent-4 that
// the operator has acknowledged, so the history grows past any one scan
// while the inbox stays within its bounds.
func agentsBoundsHistory(t *testing.T, f *offlineInboxFixture, count int) {
	t.Helper()
	f.enroll(t, (offlineSeed{}).recipients())
	for i := 1; i <= count; i++ {
		f.seq++
		spec := offlineSeed{id: fmt.Sprintf("bounds-%d", i)}
		msg := spec.message(f.seq)
		msg.From = fmt.Sprintf("agent-%d", i%5)
		msg.Payload, _ = json.Marshal(map[string]string{"text": fmt.Sprintf("text-%d", i)})
		f.persist(t, msg, spec.recipients())
		f.adopt(t, spec.id, protocol.Recipient{AgentID: "operator", SessionID: "operator-session"}, "")
	}
}

func agentsBoundsJSON(t *testing.T, socket string) agentsBoundsView {
	t.Helper()
	started := time.Now()
	code, out, stderr := agentsCall(t, socket, "--json")
	if code != 0 {
		t.Fatalf("agents exit %d after %v: %s", code, time.Since(started), stderr)
	}
	var view agentsBoundsView
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatalf("agents JSON: %v: %s", err, out)
	}
	return view
}

func agentsBoundsLast(view agentsBoundsView, id string) (found, hasMessage bool) {
	for _, agent := range view.Agents {
		if agent.ID == id {
			return true, agent.LastMessage != nil && string(*agent.LastMessage) != "null"
		}
	}
	return false, false
}

// An agent that declared work but never wrote to the operator must not
// make the command page through the whole history: beyond the scan's
// bound its last message is null and the answer says the scan was cut.
func TestOperatorAgentsBoundsQuietAgentDoesNotWalkAllHistory(t *testing.T) {
	f := newOfflineInbox(t)
	agentsBoundsHistory(t, f, 600) // far more history than any bounded scan reads
	agentsBoundsDeclare(t, f, "quiet")
	w := summaryStart(t, f)
	view := agentsBoundsJSON(t, w.operator)
	found, hasMessage := agentsBoundsLast(view, "quiet")
	if !found || hasMessage {
		t.Fatalf("quiet agent: found=%v last_message=%v", found, hasMessage)
	}
	if !view.LastMessagesTruncated {
		t.Fatal("a cut history scan must be reported as last_messages_truncated")
	}
	if found, hasMessage := agentsBoundsLast(view, "agent-0"); found && !hasMessage {
		t.Fatal("an agent with recent messages lost its last message")
	}
	code, out, stderr := agentsCall(t, w.operator)
	if code != 0 || !strings.Contains(out, "quiet") {
		t.Fatalf("text output: exit %d %q %s", code, out, stderr)
	}
}

// With a history short enough to read whole, a quiet agent's null last
// message is complete knowledge, not a cut scan.
func TestOperatorAgentsBoundsShortHistoryIsNotReportedTruncated(t *testing.T) {
	f := newOfflineInbox(t)
	summarySeed(t, f, 3)
	agentsBoundsDeclare(t, f, "quiet")
	w := summaryStart(t, f)
	view := agentsBoundsJSON(t, w.operator)
	if found, hasMessage := agentsBoundsLast(view, "quiet"); !found || hasMessage {
		t.Fatalf("quiet agent: found=%v last_message=%v", found, hasMessage)
	}
	if view.LastMessagesTruncated || view.RosterTruncated {
		t.Fatalf("complete answer reported as cut: %+v", view)
	}
}

// More declared agents than one snapshot carries: the bounded roster is
// shown, marked as cut, with how many there are; it is not a failure.
func TestOperatorAgentsBoundsLongRosterIsShownAndMarked(t *testing.T) {
	f := newOfflineInbox(t)
	summarySeed(t, f, 3)
	total := protocol.SnapshotStatuses + 6
	for i := 0; i < total; i++ {
		agentsBoundsDeclare(t, f, fmt.Sprintf("worker-%03d", i))
	}
	w := summaryStart(t, f)
	view := agentsBoundsJSON(t, w.operator)
	if !view.RosterTruncated {
		t.Fatal("a cut roster must be reported as roster_truncated")
	}
	if len(view.Agents) == 0 || len(view.Agents) > total {
		t.Fatalf("roster has %d agents", len(view.Agents))
	}
	code, out, stderr := agentsCall(t, w.operator)
	if code != 0 {
		t.Fatalf("long roster failed: exit %d %s", code, stderr)
	}
	if !strings.Contains(out, fmt.Sprint(total)) {
		t.Fatalf("text output doesn't say how many agents there are (%d): %q", total, out)
	}
}
