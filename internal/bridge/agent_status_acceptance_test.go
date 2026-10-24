package bridge

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// These wire tests intentionally use JSON rather than implementation types:
// they exercise the agreed public contract through a real broker and SQLite.
type statusRecordView struct {
	AgentID    string          `json:"agent_id"`
	SessionID  string          `json:"session_id"`
	State      string          `json:"state"`
	Task       string          `json:"task"`
	Step       string          `json:"step"`
	WaitingOn  string          `json:"waiting_on"`
	Ref        string          `json:"ref"`
	DeclaredAt time.Time       `json:"declared_at"`
	Activity   json.RawMessage `json:"activity"`
	Connected  bool            `json:"connected"`
}

type statusResponseView struct {
	Type      string            `json:"type"`
	RequestID string            `json:"request_id"`
	Code      string            `json:"code"`
	Seq       uint64            `json:"seq"`
	Status    *statusRecordView `json:"agent_status"`
}

func statusWire(t *testing.T, socket, id string) *wireAgent {
	t.Helper()
	return connectWireAgent(t, socket, protocol.Message{Type: protocol.TypeHello, AgentID: id, ProtocolVersion: protocol.Version})
}

func statusExchange(t *testing.T, w *wireAgent, frame map[string]any) statusResponseView {
	t.Helper()
	requireNoError(t, w.conn.SetDeadline(time.Now().Add(3*time.Second)))
	requireNoError(t, json.NewEncoder(w.conn).Encode(frame))
	line, err := w.reader.ReadBytes('\n')
	requireNoError(t, err)
	var out statusResponseView
	requireNoError(t, json.Unmarshal(line, &out))
	if out.RequestID != frame["request_id"] {
		t.Fatalf("uncorrelated status response: %s", line)
	}
	if out.Seq != 0 {
		t.Fatalf("status consumed a message sequence: %s", line)
	}
	return out
}

func statusSet(t *testing.T, w *wireAgent, request string, fields map[string]any) statusResponseView {
	t.Helper()
	return statusExchange(t, w, map[string]any{"type": "agent_status_set", "request_id": request, "agent_status": fields})
}

func statusGet(t *testing.T, w *wireAgent, agent string) *statusRecordView {
	t.Helper()
	out := statusExchange(t, w, map[string]any{"type": "agent_status_get", "request_id": "read", "agent_id": agent})
	if out.Type != "agent_status_result" {
		t.Fatalf("status read failed: %+v", out)
	}
	return out.Status
}

func requireStatusRecord(t *testing.T, out statusResponseView) *statusRecordView {
	t.Helper()
	if out.Type != "agent_status_result" || out.Status == nil {
		t.Fatalf("status not confirmed: %+v", out)
	}
	return out.Status
}

func TestAgentStatusAcceptanceMilestonesAndPeerRead(t *testing.T) {
	socket, _ := startBroker(t)
	w := statusWire(t, socket, "worker")
	peer := statusWire(t, socket, "reviewer")
	if got := statusGet(t, w, ""); got != nil {
		t.Fatalf("missing status was inferred: %+v", got)
	}
	before := time.Now().Add(-time.Second)
	for _, state := range []string{"active", "waiting", "blocked", "idle", "done"} {
		fields := map[string]any{"state": state, "task": "#133", "step": "review", "waiting_on": "peer", "ref": "PR 135"}
		got := requireStatusRecord(t, statusSet(t, w, state, fields))
		if got.AgentID != "worker" || got.SessionID != w.welcome.SessionID || !got.Connected {
			t.Fatalf("wrong status ownership: %+v", got)
		}
		if got.State != state || got.Task != "#133" || got.Step != "review" || got.WaitingOn != "peer" || got.Ref != "PR 135" {
			t.Fatalf("milestone fields lost: %+v", got)
		}
		if got.DeclaredAt.Before(before) || got.DeclaredAt.After(time.Now().Add(time.Second)) {
			t.Fatalf("declaration is not broker-timestamped: %+v", got)
		}
		read := statusGet(t, peer, "worker")
		if read == nil || read.State != state || !read.DeclaredAt.Equal(got.DeclaredAt) {
			t.Fatalf("read did not return confirmed milestone: %+v", read)
		}
	}
	if got := statusGet(t, peer, "never-seen"); got != nil {
		t.Fatalf("unknown peer status was inferred: %+v", got)
	}
}

func TestAgentStatusAcceptanceInvalidUpdatePreservesConfirmedRecord(t *testing.T) {
	cases := map[string]map[string]any{
		"unknown-state":   {"state": "approved"},
		"missing-state":   {"task": "#133"},
		"unknown-wait":    {"state": "waiting", "waiting_on": "arbitrary"},
		"long-task":       {"state": "active", "task": strings.Repeat("a", 121)},
		"utf8-byte-limit": {"state": "active", "step": strings.Repeat("é", 61)},
		"long-ref":        {"state": "active", "ref": strings.Repeat("x", 121)},
		"escape":          {"state": "active", "task": "\x1b[2J"},
		"newline":         {"state": "active", "step": "first\nsecond"},
		"delete-control":  {"state": "active", "ref": "a\x7fb"},
	}
	for name, fields := range cases {
		t.Run(name, func(t *testing.T) {
			socket, _ := startBroker(t)
			w := statusWire(t, socket, "worker")
			good := requireStatusRecord(t, statusSet(t, w, "good", map[string]any{"state": "active", "task": "kept"}))
			out := statusSet(t, w, "bad", fields)
			if out.Type != "error" || out.Code != "invalid_status" {
				t.Fatalf("invalid declaration accepted: %+v", out)
			}
			got := statusGet(t, w, "")
			if got == nil || got.Task != "kept" || !got.DeclaredAt.Equal(good.DeclaredAt) {
				t.Fatalf("rejected write changed stored status: %+v", got)
			}
		})
	}
}

func TestAgentStatusAcceptanceByteBoundaryAndReplacement(t *testing.T) {
	socket, _ := startBroker(t)
	w := statusWire(t, socket, "worker")
	text := strings.Repeat("é", 60)
	first := requireStatusRecord(t, statusSet(t, w, "boundary", map[string]any{"state": "waiting", "task": text, "step": text, "ref": text, "waiting_on": "ci"}))
	if first.Task != text || first.Step != text || first.Ref != text {
		t.Fatal("120-byte fields were truncated")
	}
	got := requireStatusRecord(t, statusSet(t, w, "replace", map[string]any{"state": "done"}))
	if got.Task != "" || got.Step != "" || got.Ref != "" || got.WaitingOn != "" {
		t.Fatalf("status replacement retained obsolete fields: %+v", got)
	}
}

func TestAgentStatusAcceptanceRateLimitIsExplicitAndDoesNotOverwrite(t *testing.T) {
	socket, _ := startBroker(t)
	w := statusWire(t, socket, "worker")
	last := ""
	for i := 0; i < 100; i++ {
		task := fmt.Sprintf("step-%d", i)
		out := statusSet(t, w, task, map[string]any{"state": "active", "task": task})
		if out.Type == "error" {
			if out.Code != "status_rate_limited" {
				t.Fatalf("wrong refusal: %+v", out)
			}
			got := statusGet(t, w, "")
			if got == nil || got.Task != last {
				t.Fatalf("refused status overwritten: %+v", got)
			}
			return
		}
		requireStatusRecord(t, out)
		last = task
	}
	t.Fatal("100 rapid milestone writes were not bounded")
}

func TestAgentStatusAcceptanceCrashRetainsHistoricalDeclaration(t *testing.T) {
	socket := crashSocket(t)
	stop := startCrashBroker(t, socket)
	w := statusWire(t, socket, "worker")
	old := requireStatusRecord(t, statusSet(t, w, "before-crash", map[string]any{"state": "active", "task": "#133"}))
	stop()
	w.conn.Close()
	startCrashBroker(t, socket)
	w = statusWire(t, socket, "worker")
	// A pulse from the replacement session cannot revive the old declaration.
	requireNoError(t, json.NewEncoder(w.conn).Encode(map[string]any{"type": "agent_activity", "source": "codex-launcher"}))
	got := statusGet(t, w, "")
	if got == nil || got.Task != old.Task || got.SessionID != old.SessionID || !got.DeclaredAt.Equal(old.DeclaredAt) || got.Connected {
		t.Fatalf("restart refreshed or lost historical declaration: %+v", got)
	}
	current := requireStatusRecord(t, statusSet(t, w, "new-session", map[string]any{"state": "waiting", "task": "#132", "waiting_on": "peer"}))
	if current.SessionID != w.welcome.SessionID || current.SessionID == old.SessionID || !current.Connected {
		t.Fatalf("new declaration did not transfer status ownership: %+v", current)
	}
}

func TestAgentStatusAcceptanceBrokerOwnsIdentityAndTimestamp(t *testing.T) {
	socket, _ := startBroker(t)
	w := statusWire(t, socket, "worker")
	before := time.Now().Add(-time.Second)
	fields := map[string]any{"state": "active", "agent_id": "operator", "session_id": "forged", "declared_at": "2099-01-01T00:00:00Z", "connected": false}
	out := statusExchange(t, w, map[string]any{"type": "agent_status_set", "request_id": "spoof", "agent_id": "victim", "session_id": "forged", "agent_status": fields})
	got := requireStatusRecord(t, out)
	if got.AgentID != "worker" || got.SessionID != w.welcome.SessionID || !got.Connected || got.DeclaredAt.Before(before) || got.DeclaredAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("caller controlled broker-owned status fields: %+v", got)
	}
	if statusGet(t, w, "victim") != nil || statusGet(t, w, "operator") != nil {
		t.Fatal("status write crossed agent identity")
	}
}

func TestAgentStatusAcceptanceActivityIsSeparateAndBrokerTimed(t *testing.T) {
	socket, _ := startBroker(t)
	w := statusWire(t, socket, "worker")
	declared := requireStatusRecord(t, statusSet(t, w, "declared", map[string]any{"state": "blocked", "task": "review"}))
	before := time.Now().Add(-time.Second)
	requireNoError(t, json.NewEncoder(w.conn).Encode(map[string]any{"type": "agent_activity", "source": "codex-launcher", "at": "2099-01-01T00:00:00Z"}))
	got := statusGet(t, w, "")
	if got == nil || got.State != "blocked" || !got.DeclaredAt.Equal(declared.DeclaredAt) {
		t.Fatalf("activity changed declared status: %+v", got)
	}
	var activity struct {
		At     time.Time `json:"at"`
		Source string    `json:"source"`
	}
	requireNoError(t, json.Unmarshal(got.Activity, &activity))
	if activity.Source != "codex-launcher" || activity.At.Before(before) || activity.At.After(time.Now().Add(time.Second)) {
		t.Fatalf("activity source or broker timestamp missing: %+v", activity)
	}
}
