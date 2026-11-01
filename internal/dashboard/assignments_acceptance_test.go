package dashboard

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
)

func assignmentDashboardSnapshot(t *testing.T, truncated bool) protocol.StatusSnapshot {
	t.Helper()
	snapshot := dashboardStatusSnapshot(t, true, false)
	snapshot.SchemaVersion = 3
	fields := map[string]any{"claims_available": true, "claims_total": 2, "claims_truncated": false,
		"claims": []map[string]any{{"operator_message_id": "asg-0001-parent", "claims": []any{}, "claims_total": 0, "claims_truncated": false,
			"assignments": []map[string]any{
				{"operator_message_id": "asg-0001-parent", "agent_id": "alpha", "task": "reply", "state": "assigned", "assigned_at": "2026-11-01T00:00:00Z"},
			}, "assignments_total": 1, "assignments_truncated": truncated},
			{"operator_message_id": "asg-0002-parent", "claims": []any{}, "claims_total": 0, "claims_truncated": false,
				"assignments":       []map[string]any{{"operator_message_id": "asg-0002-parent", "agent_id": "alpha", "task": "reply", "state": "done", "assigned_at": "2026-11-01T00:00:00Z", "finished_at": "2026-11-01T01:00:00Z"}},
				"assignments_total": 1, "assignments_truncated": false}}}
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestAssignmentsDashboardAcceptanceShowsAssignmentStatesAndParent(t *testing.T) {
	m := dashboardStatusModel(t, assignmentDashboardSnapshot(t, false))
	for _, row := range []int{0, 1} {
		selectStatusRow(m, row)
		requireText(t, m.details(), "reply", "assigned", "done", "asg-0001")
	}
	selectStatusRow(m, 3)
	if strings.Contains(m.details(), "asg-0001") {
		t.Fatal("another agent inherited alpha's assignment")
	}
}

func TestAssignmentsDashboardAcceptancePartialRefreshAndNarrowViews(t *testing.T) {
	m := dashboardStatusModel(t, assignmentDashboardSnapshot(t, true))
	text := strings.ToLower(strings.Join(m.claimDetails("alpha"), "\n"))
	if !strings.Contains(text, "partial") && !strings.Contains(text, "truncat") {
		t.Fatalf("assignment truncation looks complete: %s", text)
	}
	for _, size := range [][2]int{{35, 12}, {60, 18}, {120, 35}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, detail := range []bool{false, true} {
			m.detail = detail
			assertFits(t, m.View().Content, size[0], size[1])
		}
	}
	fresh := assignmentDashboardSnapshot(t, false)
	if err := json.Unmarshal([]byte(`{"claims":[],"claims_total":0}`), &fresh); err != nil {
		t.Fatal(err)
	}
	apply(m, fresh, nil)
	if strings.Contains(m.details(), "asg-0001") {
		t.Fatal("refresh retained a removed assignment")
	}
}
