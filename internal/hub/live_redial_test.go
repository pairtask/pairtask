package hub

import "testing"

// A contact that keeps failing gets at most one quick redial scheduled at a
// time, however many triggers fire, and none once the quick redials are
// spent: then the poll's backoff decides.
func TestScheduleRedialOnePendingAndBounded(t *testing.T) {
	h := newTestHub(t)
	h.msgs.init()
	const c = "sha256:contact"
	for range 20 {
		h.scheduleRedial(c)
	}
	h.live.mu.Lock()
	failures, pending := h.live.failures[c], h.live.pending[c]
	h.live.mu.Unlock()
	if failures != 1 || !pending {
		t.Fatalf("20 triggers: failures %d, pending %v; want 1 and true", failures, pending)
	}

	h.live.mu.Lock()
	delete(h.live.pending, c)
	h.live.failures[c] = liveRetriesMax
	h.live.mu.Unlock()
	h.scheduleRedial(c)
	h.live.mu.Lock()
	failures, pending = h.live.failures[c], h.live.pending[c]
	h.live.mu.Unlock()
	if failures != liveRetriesMax || pending {
		t.Fatalf("spent quick redials still scheduled one: failures %d, pending %v", failures, pending)
	}
}
