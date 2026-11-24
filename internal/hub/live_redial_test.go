package hub

import (
	"testing"
	"time"
)

// fakeTimers records scheduled redials instead of running them.
type fakeTimers struct{ fns []func() }

type fakeTimer struct{ stopped *bool }

func (t fakeTimer) Stop() bool { *t.stopped = true; return true }

func (f *fakeTimers) after(_ time.Duration, fn func()) stopper {
	f.fns = append(f.fns, fn)
	stopped := false
	return fakeTimer{&stopped}
}

func redialHub(t *testing.T) (*Hub, *fakeTimers) {
	t.Helper()
	h := newTestHub(t)
	h.msgs.init()
	f := &fakeTimers{}
	h.live.mu.Lock()
	h.live.ready()
	h.live.after = f.after
	h.live.mu.Unlock()
	return h, f
}

func redialState(h *Hub, c string) (failures int, pending bool) {
	h.live.mu.Lock()
	defer h.live.mu.Unlock()
	_, pending = h.live.pending[c]
	return h.live.failures[c], pending
}

const redialContact = "sha256:contact"

// Many triggers while a redial is scheduled schedule nothing more.
func TestScheduleRedialOnePending(t *testing.T) {
	h, f := redialHub(t)
	for range 20 {
		h.scheduleRedial(redialContact)
	}
	if failures, pending := redialState(h, redialContact); failures != 1 || !pending || len(f.fns) != 1 {
		t.Fatalf("20 triggers: failures %d, pending %v, timers %d; want 1, true, 1", failures, pending, len(f.fns))
	}
}

// Each fired redial that fails schedules the next, up to the cap; then the
// poll's backoff decides and nothing more is scheduled.
func TestScheduleRedialStopsAtTheCap(t *testing.T) {
	h, f := redialHub(t)
	for i := 0; i < liveRetriesMax+3; i++ {
		h.scheduleRedial(redialContact)
		if len(f.fns) > i {
			f.fns[i]() // fires: the redial runs (and, here, fails again)
		}
	}
	if failures, pending := redialState(h, redialContact); failures != liveRetriesMax || pending || len(f.fns) != liveRetriesMax {
		t.Fatalf("failures %d, pending %v, timers %d; want %d, false, %d", failures, pending, len(f.fns), liveRetriesMax, liveRetriesMax)
	}
}

// A reconnect cancels the outage's pending redial: a drop right after gets
// its first quick redial at once, and the old timer firing late changes
// nothing.
func TestReconnectCancelsTheOldRedial(t *testing.T) {
	h, f := redialHub(t)
	h.scheduleRedial(redialContact)
	h.live.mu.Lock()
	delete(h.live.failures, redialContact) // what goLive does on a reconnect
	h.live.cancelRedial(redialContact)
	h.live.mu.Unlock()

	h.scheduleRedial(redialContact) // the new link drops at once
	if failures, pending := redialState(h, redialContact); failures != 1 || !pending || len(f.fns) != 2 {
		t.Fatalf("after reconnect and drop: failures %d, pending %v, timers %d; want 1, true, 2", failures, pending, len(f.fns))
	}
	f.fns[0]() // the old outage's timer fires late
	if _, pending := redialState(h, redialContact); !pending {
		t.Fatal("a stale timer cleared the newer scheduled redial")
	}
}
