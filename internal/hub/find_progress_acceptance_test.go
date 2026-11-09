package hub

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func findProgressReport(t *testing.T, generation int, listed bool) protocol.Message {
	t.Helper()
	fields := map[string]any{"contact_find_generation": generation}
	if listed {
		fields["contact_found"] = []map[string]any{{"name": "Office Mac", "host": "192.168.8.20", "port": 8445}}
	}
	wire, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var report protocol.Message
	if err := json.Unmarshal(wire, &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func assertFindProgress(t *testing.T, state *findState, now time.Time, want string) {
	t.Helper()
	wire, err := json.Marshal(state.snapshot(now))
	if err != nil {
		t.Fatal(err)
	}
	var view map[string]any
	if err := json.Unmarshal(wire, &view); err != nil {
		t.Fatal(err)
	}
	if got := view["scan_state"]; got != want {
		t.Fatalf("scan state = %v, want %q; snapshot %s", got, want, wire)
	}
}

func TestFindProgressRequiresReportFromAppliedFindLease(t *testing.T) {
	now := time.Date(2026, 11, 9, 18, 0, 0, 0, time.UTC)
	var state findState
	state.want(now)
	assertFindProgress(t, &state, now, "helper_off")
	// This request arrived after Find but carries the pre-Find snapshot.
	state.helperSeen(findProgressReport(t, 0, false), now.Add(time.Second))
	assertFindProgress(t, &state, now.Add(time.Second), "pending")
	state.helperSeen(findProgressReport(t, 1, false), now.Add(2*time.Second))
	assertFindProgress(t, &state, now.Add(2*time.Second), "scanning")
	state.helperSeen(findProgressReport(t, 1, true), now.Add(3*time.Second))
	assertFindProgress(t, &state, now.Add(3*time.Second), "listed")
	state.helperSeen(findProgressReport(t, 1, false), now.Add(4*time.Second))
	assertFindProgress(t, &state, now.Add(4*time.Second), "scanning")
	if rows := state.snapshot(now.Add(4 * time.Second)).Invitations; len(rows) != 0 {
		t.Fatalf("empty report left stale invitations: %+v", rows)
	}
	assertFindProgress(t, &state, now.Add(4*time.Second+protocol.DiscoveryHelperStale), "helper_off")
}

func TestFindProgressWindowTurnoverRejectsPreviousScanGeneration(t *testing.T) {
	now := time.Date(2026, 11, 9, 18, 0, 0, 0, time.UTC)
	var state findState
	state.want(now)
	state.helperSeen(findProgressReport(t, 1, false), now.Add(time.Second))
	state.want(now.Add(5 * time.Second)) // panel refresh keeps this generation
	assertFindProgress(t, &state, now.Add(5*time.Second), "scanning")
	next := now.Add(FindWindow + time.Second)
	state.want(next)
	state.helperSeen(findProgressReport(t, 1, false), next.Add(time.Second))
	assertFindProgress(t, &state, next.Add(time.Second), "pending")
	state.helperSeen(findProgressReport(t, 2, false), next.Add(2*time.Second))
	assertFindProgress(t, &state, next.Add(2*time.Second), "scanning")
}
