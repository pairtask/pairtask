//go:build linux

package server

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

type declaredStatusView struct {
	Schema    int                    `json:"schema_version"`
	Health    string                 `json:"health"`
	Connected int                    `json:"connected_sessions"`
	Available bool                   `json:"agent_status_available"`
	Records   []protocol.AgentStatus `json:"agent_status"`
	Total     int                    `json:"agent_status_total"`
	Truncated bool                   `json:"agent_status_truncated"`
}

func readDeclaredStatus(t *testing.T, f *operatorFixture) declaredStatusView {
	t.Helper()
	p := &sessionProbe{operatorPeer: operatorDial(t, f.agent)}
	defer p.Close()
	p.write(protocol.Message{Type: "status"})
	response := p.frame()
	operatorEqual(t, "read-only reply", response.text("type"), "status")
	var out declaredStatusView
	operatorNoError(t, json.Unmarshal(response["status"], &out))
	return out
}

func TestStatusSurfacesAcceptanceCurrentAndPreviousSessionAreNotInferred(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	worker := &sessionProbe{operatorPeer: f.peer("worker", protocol.Version)}
	f.peer("silent", protocol.Version)
	worker.write(protocol.Message{Type: "agent_activity", Source: "codex-launcher"})
	result, _ := worker.request("agent_status_set", map[string]any{"agent_status": map[string]any{
		"state": "active", "task": "status surfaces", "step": "acceptance", "waiting_on": "", "ref": "#138",
	}})
	operatorEqual(t, "declaration accepted", result.text("type"), "agent_status_result")
	before := readDeclaredStatus(t, f)
	operatorEqual(t, "new status schema", before.Schema, 2)
	operatorEqual(t, "records available", before.Available, true)
	operatorEqual(t, "silent agent has no invented record", len(before.Records), 1)
	record := before.Records[0]
	operatorEqual(t, "current declaration", record.Connected, true)
	operatorEqual(t, "declared task", record.Task, "status surfaces")
	if record.Activity == nil {
		t.Fatal("reported activity missing")
	}
	operatorEqual(t, "activity source", record.Activity.Source, "codex-launcher")
	worker.Close()
	awaitStatusDisconnect(t, f)
	replacement := &sessionProbe{operatorPeer: f.peer("worker", protocol.Version)}
	replacement.write(protocol.Message{Type: "agent_activity", Source: "claude-adapter"})
	// A correlated read on this connection is a barrier for the activity frame.
	replacement.request("agent_status_get", nil)
	after := readDeclaredStatus(t, f)
	operatorEqual(t, "reconnected transport count", after.Connected, 2)
	operatorEqual(t, "one retained declaration", len(after.Records), 1)
	historical := after.Records[0]
	operatorEqual(t, "declaration remains previous-session", historical.Connected, false)
	operatorEqual(t, "declaration session unchanged", historical.SessionID, record.SessionID)
	operatorEqual(t, "state not inferred from disconnect", historical.State, "active")
	operatorEqual(t, "new session activity not attached to old declaration", historical.Activity, nil)
	operatorEqual(t, "read does not change declaration time", historical.DeclaredAt, record.DeclaredAt)
	operatorEqual(t, "observation does not register sessions", f.count("agents"), 3)
	operatorEqual(t, "observation creates no messages", f.count("messages"), 0)
}

func awaitStatusDisconnect(t *testing.T, f *operatorFixture) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if readDeclaredStatus(t, f).Connected == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("old transport did not disconnect")
}

func TestStatusSurfacesAcceptanceBoundsPrioritizeCurrentDeclaration(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	inboxExec(f, `WITH RECURSIVE ids(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM ids WHERE n<70)
INSERT INTO agent_status(agent_id,session_id,state,task,step,waiting_on,ref,declared_at)
SELECT printf('historical-%03d',n),'old','idle','','','','','2026-01-01T00:00:00Z' FROM ids`)
	worker := &sessionProbe{operatorPeer: f.peer("zzz-current", protocol.Version)}
	operatorEqual(t, "current status accepted", statusDeclare(t, worker, "waiting").text("type"), "agent_status_result")
	got := readDeclaredStatus(t, f)
	operatorEqual(t, "available page", got.Available, true)
	operatorEqual(t, "bounded records", len(got.Records), 64)
	operatorEqual(t, "full retained total", got.Total, 71)
	operatorEqual(t, "omission explicit", got.Truncated, true)
	operatorEqual(t, "current declaration first", got.Records[0].AgentID, "zzz-current")
	operatorEqual(t, "current declaration marked current", got.Records[0].Connected, true)
}

func TestStatusSurfacesAcceptanceUnavailableIsNotEmptySuccess(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	f.peer("silent", protocol.Version)
	empty := readDeclaredStatus(t, f)
	operatorEqual(t, "empty available", empty.Available, true)
	if empty.Records == nil {
		t.Fatal("known empty records must be a JSON array")
	}
	operatorEqual(t, "empty total", empty.Total, 0)
	inboxExec(f, `DROP TABLE agent_status`)
	failed := readDeclaredStatus(t, f)
	operatorEqual(t, "status read failure degrades health", failed.Health, "degraded")
	operatorEqual(t, "failure does not claim available", failed.Available, false)
	operatorEqual(t, "transport remains observable", failed.Connected, 1)
	if failed.Records == nil {
		t.Fatal("v2 unavailable records must still encode as an array")
	}
}
