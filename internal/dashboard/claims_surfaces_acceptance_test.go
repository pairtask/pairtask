package dashboard

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
)

func dashboardClaimsSnapshot(t *testing.T, available, global, parent bool) protocol.StatusSnapshot {
	t.Helper()
	snap := dashboardStatusSnapshot(t, true, false)
	snap.SchemaVersion = 3
	var claims []protocol.Claim
	for i, state := range []string{"active", "done", "released"} {
		claims = append(claims, protocol.Claim{OperatorMessageID: "12345678-parent", Task: []string{"build-claim", "verify-claim", "abandoned-claim"}[i], Topic: "claim topic", Role: "owner", AgentID: "alpha", State: state, ClaimedAt: snap.SnapshotAt.Add(-time.Hour)})
	}
	page := protocol.ClaimPage{Claims: claims, Total: 3, Truncated: parent}
	if parent {
		page.Total = 20
	}
	data, err := json.Marshal(map[string]any{"claims": []protocol.ClaimSet{{OperatorMessageID: "12345678-parent", ClaimPage: page}}, "claims_total": 1, "claims_truncated": global, "claims_available": available})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestClaimsSurfacesAcceptanceDashboardCountsOnlyActiveAndShowsAgentClaims(t *testing.T) {
	m := dashboardStatusModel(t, dashboardClaimsSnapshot(t, true, false, false))
	requireText(t, strings.Join(m.header(), "\n"), "Active claims 1")
	for _, row := range []int{0, 1} {
		selectStatusRow(m, row)
		details := m.details()
		requireText(t, details, "build-claim", "verify-claim", "owner", "active", "done", "12345678", "1h", "claim topic")
		if strings.Contains(details, "abandoned-claim") {
			t.Fatal("released claim appeared in held agent claims")
		}
	}
	selectStatusRow(m, 3)
	if strings.Contains(m.details(), "build-claim") {
		t.Fatal("alpha's claim appeared under beta")
	}
}

func TestClaimsSurfacesAcceptanceDashboardMarksBothPartialLevels(t *testing.T) {
	for _, global := range []bool{false, true} {
		name := "per parent"
		if global {
			name = "global"
		}
		t.Run(name, func(t *testing.T) {
			m := dashboardStatusModel(t, dashboardClaimsSnapshot(t, true, global, !global))
			requireText(t, strings.Join(m.header(), "\n"), "Active claims 1+")
			detail := strings.ToLower(m.details())
			if !strings.Contains(detail, "truncat") && !strings.Contains(detail, "partial") && !strings.Contains(detail, "omitted") {
				t.Fatalf("partial claims looked complete: %s", detail)
			}
		})
	}
}

func TestClaimsSurfacesAcceptanceDashboardUnavailableDoesNotClaimZero(t *testing.T) {
	m := dashboardStatusModel(t, dashboardClaimsSnapshot(t, false, false, false))
	header := strings.ToLower(strings.Join(m.header(), "\n"))
	requireText(t, header, "claims", "unavailable")
	if strings.Contains(header, "active claims 0") || strings.Contains(header, "active claims 1") {
		t.Fatal("unavailable claims displayed as an observed count")
	}
}

func TestClaimsSurfacesAcceptanceDashboardRefreshAndWidth(t *testing.T) {
	snap := dashboardClaimsSnapshot(t, true, false, false)
	m := dashboardStatusModel(t, snap)
	if err := json.Unmarshal([]byte(`{"claims":[],"claims_total":0}`), &snap); err != nil {
		t.Fatal(err)
	}
	apply(m, snap, nil)
	requireText(t, strings.Join(m.header(), "\n"), "Active claims 0")
	if strings.Contains(m.details(), "build-claim") {
		t.Fatal("refresh retained a removed claim")
	}
	apply(m, dashboardClaimsSnapshot(t, true, true, true), nil)
	for _, size := range [][2]int{{35, 12}, {60, 18}, {120, 35}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.detail = true
		assertFits(t, m.View().Content, size[0], size[1])
		m.detail = false
		assertFits(t, m.View().Content, size[0], size[1])
	}
}
