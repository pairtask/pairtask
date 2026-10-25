package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

const statusFullRef = "0123456789abcdef0123456789abcdef01234567"

func statusSurfaceWire(t *testing.T) map[string]any {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	snapshot := readyStatus()
	snapshot.SnapshotAt = &now
	snapshot.Sessions = []protocol.SessionStatus{
		{AgentID: "current", SessionID: "current-session", State: "transport_connected"},
		{AgentID: "returning", SessionID: "new-session", State: "transport_connected"},
		{AgentID: "silent", SessionID: "silent-session", State: "transport_connected"},
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	out["schema_version"], out["agent_status_available"] = 2, true
	out["agent_status_total"], out["agent_status_truncated"] = 2, false
	out["agent_status"] = []protocol.AgentStatus{
		{AgentID: "current", SessionID: "current-session", State: "waiting", WaitingOn: "ci", Task: "status-task", Step: "review-step", Ref: statusFullRef, DeclaredAt: now.Add(-2 * time.Hour), Connected: true, Activity: &protocol.Activity{At: now.Add(-3 * time.Minute), Source: "codex-launcher"}},
		{AgentID: "returning", SessionID: "old-session", State: "active", Task: "previous-task", DeclaredAt: now.Add(-24 * time.Hour), Connected: false},
	}
	return out
}

func serveStatusSurface(t *testing.T, snapshot map[string]any) string {
	t.Helper()
	return statusSocket(t, func(c net.Conn) {
		var req protocol.Message
		if err := protocol.ReadFrame(bufio.NewReader(c), &req); err != nil {
			t.Error(err)
			return
		}
		if req.Type != "status" || req.AgentID != "" {
			t.Errorf("not a read-only request: %+v", req)
		}
		if err := json.NewEncoder(c).Encode(map[string]any{"type": "status", "status": snapshot}); err != nil {
			t.Error(err)
		}
	})
}

func statusSurfaceText(t *testing.T, snapshot map[string]any) string {
	t.Helper()
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"status", "--socket", serveStatusSurface(t, snapshot)}, &out, &stderr)
	assertStatusEqual(t, "text exit", code, 0)
	return out.String()
}

func requireStatusSurfaceText(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func TestStatusSurfacesAcceptanceJSONPreservesDeclarations(t *testing.T) {
	code, out := runJSON(t, "--socket", serveStatusSurface(t, statusSurfaceWire(t)))
	assertStatusEqual(t, "JSON exit", code, 0)
	assertJSONField(t, out, "schema_version", float64(2))
	assertJSONField(t, out, "agent_status_available", true)
	assertJSONField(t, out, "agent_status_total", float64(2))
	assertJSONField(t, out, "agent_status_truncated", false)
	records, ok := out["agent_status"].([]any)
	if !ok {
		t.Fatal("declarations missing from JSON")
	}
	assertStatusEqual(t, "JSON record count", len(records), 2)
	current := records[0].(map[string]any)
	assertJSONField(t, current, "ref", statusFullRef)
	assertJSONField(t, current, "connected", true)
	previous := records[1].(map[string]any)
	assertJSONField(t, previous, "session_id", "old-session")
	assertJSONField(t, previous, "connected", false)
	assertJSONField(t, previous, "activity", nil)
}

func TestStatusSurfacesAcceptanceTextShowsAgesAndPreviousSession(t *testing.T) {
	text := statusSurfaceText(t, statusSurfaceWire(t))
	requireStatusSurfaceText(t, text, "Agent status", "shown 2 of 2", "status-task", "review-step", "ci", statusFullRef[:12], "codex-launcher", "current", "previous", "previous-task", "no status declared", "unavailable", "2h", "3m")
	if strings.Contains(text, "blocked") {
		t.Fatal("old declaration invented blocked state")
	}
	if strings.Contains(text, statusFullRef) {
		t.Fatal("full commit was not shortened for text")
	}
}

func TestStatusSurfacesAcceptanceTruncatedAbsenceIsNotNoDeclaration(t *testing.T) {
	snapshot := statusSurfaceWire(t)
	snapshot["agent_status_total"], snapshot["agent_status_truncated"] = 90, true
	text := statusSurfaceText(t, snapshot)
	requireStatusSurfaceText(t, text, "shown 2 of 90", "truncated: true")
	if strings.Contains(text, "no status declared") {
		t.Fatal("truncated absence presented as no declaration")
	}
}

func TestStatusSurfacesAcceptanceLegacySchemaRemainsReadable(t *testing.T) {
	snapshot := statusSurfaceWire(t)
	snapshot["schema_version"] = 1
	for _, key := range []string{"agent_status", "agent_status_total", "agent_status_truncated", "agent_status_available"} {
		delete(snapshot, key)
	}
	text := statusSurfaceText(t, snapshot)
	requireStatusSurfaceText(t, text, "Broker: ready", "Agent status: unavailable")
	code, out := runJSON(t, "--socket", serveStatusSurface(t, snapshot))
	assertStatusEqual(t, "legacy JSON exit", code, 0)
	assertJSONField(t, out, "schema_version", float64(1))
	assertJSONField(t, out, "agent_status_available", false)
}

func TestStatusSurfacesAcceptanceTextEscapesDeclarationFields(t *testing.T) {
	snapshot := statusSurfaceWire(t)
	records := snapshot["agent_status"].([]protocol.AgentStatus)
	records[0].Task = "unsafe\x1b[2J\nnext-line"
	text := statusSurfaceText(t, snapshot)
	requireStatusSurfaceText(t, text, `unsafe\x1b[2J\nnext-line`)
	if strings.ContainsRune(text, '\x1b') {
		t.Fatal("status field injected terminal control")
	}
}

func TestStatusSurfacesAcceptanceLegacySchemaDiscardsDeclarations(t *testing.T) {
	snapshot := statusSurfaceWire(t)
	snapshot["schema_version"] = 1
	snapshot["agent_status_truncated"] = true
	code, out := runJSON(t, "--socket", serveStatusSurface(t, snapshot))
	assertStatusEqual(t, "legacy JSON exit", code, 0)
	assertJSONField(t, out, "agent_status_available", false)
	assertJSONField(t, out, "agent_status_total", float64(0))
	assertJSONField(t, out, "agent_status_truncated", false)
	records, ok := out["agent_status"].([]any)
	if !ok || len(records) != 0 {
		t.Fatalf("schema 1 must expose an empty declaration array, got %#v", out["agent_status"])
	}
}
