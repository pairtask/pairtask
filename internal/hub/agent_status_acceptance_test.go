package hub

import (
	"testing"
	"time"
)

func TestAgentStatusLimiterAllowsMilestoneBurstAndRefillsPerAgent(t *testing.T) {
	var statuses statusState
	statuses.init()
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	// Ordered phases exercise one shared bucket across the full timeline.
	phases := []struct {
		name, agent string
		after       time.Duration
		count       int
		allowed     bool
	}{
		{"initial burst", "builder", 0, 10, true},
		{"exhausted", "builder", 0, 1, false},
		{"independent agent", "reviewer", 0, 1, true},
		{"before refill", "builder", time.Second, 1, false},
		{"refilled token", "builder", 2 * time.Second, 1, true},
		{"single refill only", "builder", 2 * time.Second, 1, false},
		{"restored burst", "builder", time.Hour, 10, true},
		{"bounded idle credit", "builder", time.Hour, 1, false},
	}
	for _, phase := range phases {
		t.Run(phase.name, func(t *testing.T) {
			for i := 0; i < phase.count; i++ {
				if got := statuses.take(phase.agent, now.Add(phase.after)); got != phase.allowed {
					t.Fatalf("update %d allowed=%t, want %t", i+1, got, phase.allowed)
				}
			}
		})
	}
}
