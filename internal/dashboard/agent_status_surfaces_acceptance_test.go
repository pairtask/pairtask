package dashboard

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
)

const dashboardFullRef = "0123456789abcdef0123456789abcdef01234567"

func dashboardStatusSnapshot(t *testing.T, available, truncated bool) protocol.StatusSnapshot {
	t.Helper()
	snapshot := ready()
	snapshot.SchemaVersion = 2
	snapshot.Sessions = []protocol.SessionStatus{
		{AgentID: "alpha", SessionID: "new-alpha", State: "transport_connected"},
		{AgentID: "alpha", SessionID: "old-alpha", State: "disconnected"},
		{AgentID: "alpha", SessionID: "unrelated-alpha", State: "disconnected"},
		{AgentID: "beta", SessionID: "beta-session", State: "transport_connected"},
		{AgentID: "silent", SessionID: "silent-session", State: "transport_connected"},
	}
	now := *snapshot.SnapshotAt
	records := []protocol.AgentStatus{
		{AgentID: "alpha", SessionID: "old-alpha", State: "active", Task: "previous-task", Step: "old-step", Ref: dashboardFullRef, DeclaredAt: now.Add(-2 * time.Hour)},
		{AgentID: "beta", SessionID: "beta-session", State: "waiting", Task: "current-task", Step: "current-step", WaitingOn: "ci", Ref: dashboardFullRef, DeclaredAt: now.Add(-time.Hour), Connected: true, Activity: &protocol.Activity{At: now.Add(-3 * time.Minute), Source: "codex-launcher"}},
	}
	total := len(records)
	if truncated {
		total = 80
	}
	data, err := json.Marshal(map[string]any{"agent_status": records, "agent_status_total": total, "agent_status_truncated": truncated, "agent_status_available": available})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func dashboardStatusModel(t *testing.T, snapshot protocol.StatusSnapshot) *model {
	t.Helper()
	m := fixture(t, nil)
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	apply(m, snapshot, nil)
	return m
}

func selectStatusRow(m *model, index int) {
	m.cursor = index
	m.selectRow(m.rows())
}

func TestStatusSurfacesAcceptanceDashboardKeepsDeclarationSession(t *testing.T) {
	m := dashboardStatusModel(t, dashboardStatusSnapshot(t, true, false))
	previous := strings.ToLower(m.details())
	requireText(t, previous, "previous-task", "previous", "active", "unavailable", "2h")
	if strings.Contains(previous, "blocked") {
		t.Fatal("old declaration inferred blocked")
	}
	roster := strings.Join(m.roster(viewport{100, 30}), "\n")
	requireText(t, roster, "previous:", "waiting")
	selectStatusRow(m, 2)
	if strings.Contains(m.details(), "previous-task") {
		t.Fatal("declaration attached to unrelated historical session")
	}
	selectStatusRow(m, 3)
	requireText(t, m.details(), "current-task", "current-step", "ci", dashboardFullRef, "codex-launcher", "3m")
}

func TestStatusSurfacesAcceptanceDashboardDistinguishesMissingAndUnknown(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		available, truncated bool
		want                 string
	}{
		{"complete", true, false, "no status"},
		{"truncated", true, true, "unknown"},
		{"unavailable", false, false, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := dashboardStatusModel(t, dashboardStatusSnapshot(t, tc.available, tc.truncated))
			m.filter = "silent"
			m.syncSelection()
			roster := strings.ToLower(strings.Join(m.roster(viewport{100, 30}), "\n"))
			requireText(t, roster, tc.want)
			if tc.truncated {
				requireText(t, strings.ToLower(m.notices()), "status", "truncated")
			}
		})
	}
}

func TestStatusSurfacesAcceptanceDashboardRefreshesAndRetainsExplicitlyStale(t *testing.T) {
	snapshot := dashboardStatusSnapshot(t, true, false)
	m := dashboardStatusModel(t, snapshot)
	selectStatusRow(m, 3)
	update := `{"agent_status":[{"agent_id":"beta","session_id":"beta-session","state":"done","task":"completed-task","declared_at":"2026-10-25T00:00:00Z","connected":true}],"agent_status_total":1}`
	if err := json.Unmarshal([]byte(update), &snapshot); err != nil {
		t.Fatal(err)
	}
	apply(m, snapshot, nil)
	requireText(t, m.details(), "completed-task", "done")
	if strings.Contains(m.details(), "current-task") {
		t.Fatal("refresh retained superseded declaration")
	}
	apply(m, protocol.UnavailableStatus(), errors.New("offline"))
	requireText(t, m.details(), "completed-task", "STALE")
}

func TestStatusSurfacesAcceptanceDashboardFitsAndEscapesStatusText(t *testing.T) {
	snapshot := dashboardStatusSnapshot(t, true, false)
	// Treat incoming fields as untrusted even though normal writes validate them.
	data, _ := json.Marshal(map[string]any{"agent_status": []protocol.AgentStatus{{AgentID: "alpha", SessionID: "new-alpha", State: "active", Task: "unsafe\x1b[2J\r\u202e", Step: strings.Repeat("界", 100), Ref: dashboardFullRef, DeclaredAt: time.Now(), Connected: true}}})
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	m := dashboardStatusModel(t, snapshot)
	if strings.ContainsAny(m.details(), "\x1b\r\u202e") {
		t.Fatal("status detail exposed terminal controls")
	}
	for _, size := range [][2]int{{35, 12}, {60, 18}, {120, 35}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		assertFits(t, m.View().Content, size[0], size[1])
		m.detail = true
		assertFits(t, m.View().Content, size[0], size[1])
		m.detail = false
	}
}
