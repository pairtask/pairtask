package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/store"
)

func TestSpacePinsAcceptancePersistAndRejectTrustChanges(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pins.db")
	s, err := store.Open(path)
	spacePinCheck(t, err == nil, "%v", err)
	t.Cleanup(func() {
		if s != nil {
			_ = s.Close()
		}
	})
	p := space.Pin{Space: "0123456789abcdef0123456789abcdef", Owner: "sha256:" + strings.Repeat("a", 64), Base: "https://directory.example", JoinedAt: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	if err := s.PinSpace(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = nil
	s, err = store.Open(path)
	spacePinCheck(t, err == nil, "%v", err)
	same := p
	same.JoinedAt = p.JoinedAt.Add(time.Hour)
	if err := s.PinSpace(ctx, same); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.SpacePin(ctx, p.Space)
	spacePinCheck(t, err == nil, "pin lost on reopen: %v %v", ok, err)
	spacePinCheck(t, ok, "pin lost on reopen: %v %v", ok, err)
	spacePinCheck(t, got.Owner == p.Owner, "idempotent pin replaced trust or first-join time: %+v", got)
	spacePinCheck(t, got.Base == p.Base, "idempotent pin replaced trust or first-join time: %+v", got)
	spacePinCheck(t, got.JoinedAt.Equal(p.JoinedAt), "idempotent pin replaced trust or first-join time: %+v", got)
	for _, changed := range []space.Pin{
		{Space: p.Space, Owner: "sha256:" + strings.Repeat("b", 64), Base: p.Base, JoinedAt: p.JoinedAt},
		{Space: p.Space, Owner: p.Owner, Base: "https://attacker.example", JoinedAt: p.JoinedAt},
	} {
		if err := s.PinSpace(ctx, changed); !errors.Is(err, space.ErrPinConflict) {
			t.Fatalf("trust change allowed: %v", err)
		}
	}
	got, ok, err = s.SpacePin(ctx, p.Space)
	spacePinCheck(t, err == nil, "conflict changed existing pin")
	spacePinCheck(t, ok, "conflict changed existing pin")
	spacePinCheck(t, got.Owner == p.Owner, "conflict changed existing pin")
	spacePinCheck(t, got.Base == p.Base, "conflict changed existing pin")
}

func TestSpacePinsAcceptanceListAndUnpin(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "pins.db"))
	spacePinCheck(t, err == nil, "%v", err)
	defer s.Close()
	a := space.Pin{Space: strings.Repeat("a", 32), Owner: "sha256:" + strings.Repeat("a", 64), Base: "https://directory.example", JoinedAt: time.Now().UTC()}
	b := a
	b.Space = strings.Repeat("b", 32)
	for _, p := range []space.Pin{b, a} {
		if err := s.PinSpace(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	pins, err := s.SpacePins(ctx)
	spacePinCheck(t, err == nil, "%v", err)
	spacePinCheck(t, len(pins) == 2, "pins not ordered: %+v", pins)
	spacePinCheck(t, pins[0].Space == a.Space, "pins not ordered: %+v", pins)
	spacePinCheck(t, pins[1].Space == b.Space, "pins not ordered: %+v", pins)
	if err := s.UnpinSpace(ctx, a.Space); err != nil {
		t.Fatal(err)
	}
	if err := s.UnpinSpace(ctx, a.Space); err != nil {
		t.Fatal(err)
	}
	_, ok, err := s.SpacePin(ctx, a.Space)
	spacePinCheck(t, err == nil, "pin not removed")
	spacePinCheck(t, !ok, "pin not removed")
	_, ok, err = s.SpacePin(ctx, b.Space)
	spacePinCheck(t, err == nil, "unpin removed unrelated space")
	spacePinCheck(t, ok, "unpin removed unrelated space")
}

func spacePinCheck(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}
