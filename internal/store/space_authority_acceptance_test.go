package store_test

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/pairbook"
)

type authorityTimer struct {
	delay   time.Duration
	fire    func()
	stopped bool
}

type authorityClock struct {
	now    time.Time
	timers []*authorityTimer
}

func (c *authorityClock) timing() hub.SpaceTiming {
	return hub.SpaceTiming{Now: func() time.Time { return c.now }, After: func(d time.Duration, f func()) func() bool {
		timer := &authorityTimer{delay: d, fire: f}
		c.timers = append(c.timers, timer)
		return func() bool { was := timer.stopped; timer.stopped = true; return !was }
	}}
}

func TestHubAuthorityRenewalAndIndependentExpiry(t *testing.T) {
	ctx := context.Background()
	key, m := mapRecord(t)
	first := pairbook.SignSpaceMap(key, m)
	s := openMapStore(t, filepath.Join(t.TempDir(), "authority.db"))
	defer s.Close()
	pinMap(t, s, first)
	h := hub.New(s, 0, slog.New(slog.DiscardHandler))
	clock := authorityClock{now: mapTime}
	h.SetSpaceTiming(clock.timing())
	var notices []uint64
	h.OnSpaceChange(func(id string) {
		current, active := h.SpaceAuthority(id)
		if active {
			persisted, mark, ok, err := s.SpaceMap(ctx, id)
			spacePinCheck(t, err == nil && ok, "activation without persisted state")
			spacePinCheck(t, mark == current.Mark && mark == persisted.Mark(), "activation preceded map commit")
		}
		notices = append(notices, current.Mark.Version)
	})
	err := h.KeepSpaceMap(ctx, mapSpace, first)
	spacePinCheck(t, err == nil, "keep first: %v", err)
	err = h.KeepSpaceMap(ctx, mapSpace, first)
	spacePinCheck(t, err == nil, "repeat first: %v", err)
	spacePinCheck(t, len(clock.timers) == 1, "idempotent refresh changed timer")
	m.Version++
	m.ExpiresAt += time.Hour.Milliseconds()
	second := pairbook.SignSpaceMap(key, m)
	err = h.KeepSpaceMap(ctx, mapSpace, second)
	spacePinCheck(t, err == nil, "renew: %v", err)
	spacePinCheck(t, len(clock.timers) == 2, "missing renewal timer")
	spacePinCheck(t, clock.timers[0].stopped, "superseded timer still running")
	clock.now = time.UnixMilli(first.ExpiresAt)
	clock.timers[0].fire()
	active, ok := h.SpaceAuthority(mapSpace)
	spacePinCheck(t, ok && active.Mark == second.Mark(), "old timer removed new map")
	err = h.KeepSpaceMap(ctx, mapSpace, first)
	spacePinCheck(t, errors.Is(err, pairbook.ErrExpired), "stale refresh after expiry: %v", err)
	spacePinCheck(t, len(clock.timers) == 2, "failed refresh moved deadline")
	clock.now = time.UnixMilli(second.ExpiresAt)
	_, ok = h.SpaceAuthority(mapSpace)
	spacePinCheck(t, !ok, "authority granted at expiry before callback")
	clock.timers[1].fire()
	spacePinCheck(t, len(notices) == 3, "wrong notice count %v", notices)
	spacePinCheck(t, notices[0] == 1 && notices[1] == 2 && notices[2] == 0, "wrong authority changes %v", notices)
	checkHeldMap(t, s, second)
}

func TestAuthorityChangeHandlerCanRefreshSameMap(t *testing.T) {
	ctx := context.Background()
	k, m := mapRecord(t)
	record := pairbook.SignSpaceMap(k, m)
	s := openMapStore(t, filepath.Join(t.TempDir(), "callback.db"))
	defer s.Close()
	pinMap(t, s, record)
	h := hub.New(s, 0, slog.New(slog.DiscardHandler))
	clock := authorityClock{now: mapTime}
	h.SetSpaceTiming(clock.timing())
	inner := make(chan error, 1)
	h.OnSpaceChange(func(id string) { inner <- h.KeepSpaceMap(ctx, id, record) })
	done := make(chan error, 1)
	go func() { done <- h.KeepSpaceMap(ctx, mapSpace, record) }()
	select {
	case err := <-done:
		spacePinCheck(t, err == nil, "outer keep: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("change callback could not reenter KeepSpaceMap")
	}
	select {
	case err := <-inner:
		spacePinCheck(t, err == nil, "reentrant refresh: %v", err)
	default:
		t.Fatal("map update returned without notifying listener")
	}
}

func TestHubRestoresMapAndCancelsTimerOnStop(t *testing.T) {
	ctx := context.Background()
	key, m := mapRecord(t)
	signed := pairbook.SignSpaceMap(key, m)
	path := filepath.Join(t.TempDir(), "reopen.db")
	s := openMapStore(t, path)
	pinMap(t, s, signed)
	_, err := s.KeepSpaceMap(ctx, mapSpace, signed, mapTime)
	spacePinCheck(t, err == nil, "persist: %v", err)
	err = s.Close()
	spacePinCheck(t, err == nil, "close: %v", err)
	s = openMapStore(t, path)
	defer s.Close()
	h := hub.New(s, 0, slog.New(slog.DiscardHandler))
	clock := authorityClock{now: mapTime}
	h.SetSpaceTiming(clock.timing())
	notice := make(chan string, 3)
	h.OnSpaceChange(func(id string) { notice <- id })
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	go h.Run(run)
	select {
	case <-notice:
	case <-time.After(3 * time.Second):
		t.Fatal("map never restored on startup")
	}
	v, ok := h.SpaceAuthority(mapSpace)
	spacePinCheck(t, ok && v.Mark == signed.Mark(), "restored wrong authority")
	cancel()
	<-h.Done()
	spacePinCheck(t, clock.timers[0].stopped, "timer remained active after shutdown")
	clock.timers[0].fire()
	spacePinCheck(t, len(notice) == 0, "cancelled timer notified after shutdown")
	checkHeldMap(t, s, signed)
}

func TestAuthorityStopsPendingListenersOnShutdown(t *testing.T) {
	ctx := context.Background()
	key, m := mapRecord(t)
	record := pairbook.SignSpaceMap(key, m)
	s := openMapStore(t, filepath.Join(t.TempDir(), "stop.db"))
	defer s.Close()
	pinMap(t, s, record)
	h := hub.New(s, 0, slog.New(slog.DiscardHandler))
	clock := authorityClock{now: mapTime}
	h.SetSpaceTiming(clock.timing())
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	go h.Run(run)
	started, release := make(chan struct{}), make(chan struct{})
	h.OnSpaceChange(func(string) { close(started); <-release })
	late := make(chan bool, 1)
	h.OnSpaceChange(func(string) {
		select {
		case <-h.Done():
			late <- true
		default:
			late <- false
		}
	})
	kept := make(chan error, 1)
	go func() { kept <- h.KeepSpaceMap(ctx, mapSpace, record) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("listener did not start")
	}
	cancel()
	select {
	case <-h.Done():
	case <-time.After(250 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-kept:
		spacePinCheck(t, err == nil, "map update: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("listener did not return")
	}
	<-h.Done()
	select {
	case afterStop := <-late:
		spacePinCheck(t, !afterStop, "listener started after Hub.Done closed")
	default:
	}
}
