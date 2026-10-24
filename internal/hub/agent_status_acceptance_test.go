package hub

import (
	"testing"
	"time"
)

func TestAgentStatusLimiterAllowsMilestoneBurstAndRefillsPerAgent(t *testing.T) {
	var statuses statusState
	statuses.init()
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		if !statuses.take("builder", now) {
			t.Fatalf("milestone %d refused within ten-update burst", i+1)
		}
	}
	if statuses.take("builder", now) {
		t.Fatal("exhausted burst accepted another update")
	}
	if !statuses.take("reviewer", now) {
		t.Fatal("one agent exhausted another agent's allowance")
	}
	if statuses.take("builder", now.Add(time.Second)) {
		t.Fatal("one token refilled before two seconds")
	}
	if !statuses.take("builder", now.Add(2*time.Second)) {
		t.Fatal("next milestone refused after refill")
	}
	if statuses.take("builder", now.Add(2*time.Second)) {
		t.Fatal("single refill admitted two updates")
	}
	// A long pause restores a bounded burst, not accumulated unbounded credit.
	for i := 0; i < 10; i++ {
		if !statuses.take("builder", now.Add(time.Hour)) {
			t.Fatal("idle agent's burst was not restored")
		}
	}
	if statuses.take("builder", now.Add(time.Hour)) {
		t.Fatal("idle credit exceeded the burst bound")
	}
}
