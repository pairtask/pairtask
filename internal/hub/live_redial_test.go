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

// redialWant is the redial state a test expects for the contact.
type redialWant struct {
	failures, timers int
	pending          bool
}

func wantRedial(t *testing.T, h *Hub, f *fakeTimers, want redialWant) {
	t.Helper()
	h.live.mu.Lock()
	_, pending := h.live.pending[redialContact]
	got := redialWant{failures: h.live.failures[redialContact], timers: len(f.fns), pending: pending}
	h.live.mu.Unlock()
	if got != want {
		t.Fatalf("redial state %+v, want %+v", got, want)
	}
}

const redialContact = "sha256:contact"

// Many triggers while a redial is scheduled schedule nothing more.
func TestScheduleRedialOnePending(t *testing.T) {
	h, f := redialHub(t)
	for range 20 {
		h.scheduleRedial(redialContact)
	}
	wantRedial(t, h, f, redialWant{failures: 1, timers: 1, pending: true})
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
	wantRedial(t, h, f, redialWant{failures: liveRetriesMax, timers: liveRetriesMax})
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
	wantRedial(t, h, f, redialWant{failures: 1, timers: 2, pending: true})
	f.fns[0]() // the old outage's timer fires late: it must not clear the newer one
	wantRedial(t, h, f, redialWant{failures: 1, timers: 2, pending: true})
}
