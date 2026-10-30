package hub

import (
	"fmt"
	"testing"
	"time"
)

func TestLiveAcceptanceReplacementAtCapacityKeepsContactOwned(t *testing.T) {
	h := newTestHub(t)
	links := make([]*liveLink, 0, 12)
	for n := 0; n < 12; n++ {
		l, ok := h.claimLive(fmt.Sprintf("contact-%d", n), true)
		if !ok {
			t.Fatalf("live contact %d refused below deployed capacity", n)
		}
		links = append(links, l)
	}
	if _, ok := h.claimLive("overflow", true); ok {
		t.Fatal("live pool consumed reserved pairing capacity")
	}
	replacement, ok := h.claimLive("contact-0", true)
	if !ok {
		t.Fatal("full pool refused replacement of its own contact")
	}
	select {
	case <-links[0].stop:
	default:
		t.Fatal("superseded link was not stopped")
	}
	h.releaseLive("contact-0", links[0])
	if !h.Live("contact-0") {
		t.Fatal("old link cleanup erased replacement")
	}
	if _, ok := h.claimLive("overflow", true); ok {
		t.Fatal("old cleanup incorrectly freed replacement's capacity")
	}
	h.releaseLive("contact-0", replacement)
	if h.Live("contact-0") {
		t.Fatal("closed replacement still owns contact")
	}
	if _, ok := h.claimLive("new-contact", true); !ok {
		t.Fatal("released live capacity was not reusable")
	}
}

func TestLiveAcceptanceReconnectJitterStaysWithinMaximum(t *testing.T) {
	h := newTestHub(t)
	first := h.liveDelay("contact")
	if first < 0 || first > time.Second {
		t.Fatalf("first reconnect outside agreed one-second bound: %v", first)
	}
	for n := 0; n < 100; n++ {
		delay := h.liveDelay("contact")
		if delay < 0 || delay > time.Minute {
			t.Fatalf("reconnect jitter exceeded one-minute cap: %v", delay)
		}
	}
}
