package store_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/store"
	"github.com/pairtask/pairtask/pairbook"
)

var mapTime = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

const mapSpace = "0123456789abcdef0123456789abcdef"

func mapRecord(t *testing.T) (ed25519.PrivateKey, pairbook.SpaceMap) {
	t.Helper()
	p, k, err := ed25519.GenerateKey(rand.Reader)
	spacePinCheck(t, err == nil, "make key: %v", err)
	a := identity.Anchor{Person: p, InitAt: mapTime.Add(-time.Hour)}.Signed(k)
	owner := pairbook.MapPerson{Lineage: pairbook.Lineage(a.Lineage()), Person: p, Name: "Owner", Role: pairbook.RoleOwner, Evidence: pairbook.PersonEvidence{Anchor: a.Marshal()}}
	return k, pairbook.SpaceMap{Space: mapSpace, Version: 1, IssuedAt: mapTime.UnixMilli(), ExpiresAt: mapTime.Add(time.Hour).UnixMilli(), People: []pairbook.MapPerson{owner}}
}

func openMapStore(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(path)
	spacePinCheck(t, err == nil, "open map store: %v", err)
	return s
}

func pinMap(t *testing.T, s *store.Store, m pairbook.SignedSpaceMap) {
	t.Helper()
	p := space.Pin{Space: m.Space, Owner: pairbook.KeyID(m.Owner), Base: "https://directory.example", JoinedAt: mapTime}
	err := s.PinSpace(context.Background(), p)
	spacePinCheck(t, err == nil, "pin: %v", err)
}

func checkHeldMap(t *testing.T, s *store.Store, want pairbook.SignedSpaceMap) {
	t.Helper()
	got, mark, ok, err := s.SpaceMap(context.Background(), want.Space)
	spacePinCheck(t, err == nil, "read: %v", err)
	spacePinCheck(t, ok, "missing map")
	spacePinCheck(t, mark == want.Mark(), "stored mark differs")
	spacePinCheck(t, bytes.Equal(got.SigningBytes(), want.SigningBytes()), "stored map differs")
	spacePinCheck(t, bytes.Equal(got.Sig, want.Sig), "stored signature differs")
}

func TestStoredMapSurvivesRestartAndExpiry(t *testing.T) {
	ctx := context.Background()
	k, m := mapRecord(t)
	m.Version = math.MaxUint64
	signed := pairbook.SignSpaceMap(k, m)
	path := filepath.Join(t.TempDir(), "maps.db")
	s := openMapStore(t, path)
	pinMap(t, s, signed)
	_, err := s.KeepSpaceMap(ctx, m.Space, signed, mapTime)
	closeErr := s.Close()
	spacePinCheck(t, err == nil, "keep full unsigned version: %v", err)
	spacePinCheck(t, closeErr == nil, "close: %v", closeErr)
	s = openMapStore(t, path)
	defer s.Close()
	checkHeldMap(t, s, signed)
	_, err = s.KeepSpaceMap(ctx, m.Space, signed, time.UnixMilli(m.ExpiresAt))
	spacePinCheck(t, errors.Is(err, pairbook.ErrExpired), "expired map: %v", err)
	m.Version--
	_, err = s.KeepSpaceMap(ctx, m.Space, pairbook.SignSpaceMap(k, m), mapTime)
	spacePinCheck(t, errors.Is(err, pairbook.ErrRollback), "older map: %v", err)
	checkHeldMap(t, s, signed)
}

func TestMapConflictAndUnpin(t *testing.T) {
	ctx := context.Background()
	k, m := mapRecord(t)
	signed := pairbook.SignSpaceMap(k, m)
	s := openMapStore(t, filepath.Join(t.TempDir(), "maps.db"))
	defer s.Close()
	pinMap(t, s, signed)
	for range 2 {
		_, err := s.KeepSpaceMap(ctx, m.Space, signed, mapTime)
		spacePinCheck(t, err == nil, "idempotent keep: %v", err)
	}
	m.ExpiresAt--
	_, err := s.KeepSpaceMap(ctx, m.Space, pairbook.SignSpaceMap(k, m), mapTime)
	spacePinCheck(t, errors.Is(err, pairbook.ErrRollback), "divergent map: %v", err)
	checkHeldMap(t, s, signed)
	err = s.UnpinSpace(ctx, m.Space)
	spacePinCheck(t, err == nil, "unpin: %v", err)
	_, _, ok, err := s.SpaceMap(ctx, m.Space)
	spacePinCheck(t, err == nil, "read after unpin: %v", err)
	spacePinCheck(t, !ok, "unpin retained map")
}
