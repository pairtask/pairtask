package store_test

import (
	"context"
	"crypto"
	"errors"
	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
	"log/slog"
	"path/filepath"
	"testing"
)

type reviewDirectory struct {
	space.MapDirectory
	reply pairbook.SignedSpaceMap
	err   error
}

func (d *reviewDirectory) Map(context.Context, space.Pin) (pairbook.SignedSpaceMap, error) {
	return d.reply, d.err
}
func TestReviewRefreshRecovery(t *testing.T) {
	key, m := mapRecord(t)
	signed := pairbook.SignSpaceMap(key, m)
	s := openMapStore(t, filepath.Join(t.TempDir(), "refresh.db"))
	defer s.Close()
	pinMap(t, s, signed)
	h := hub.New(s, 0, slog.New(slog.DiscardHandler))
	clock := authorityClock{now: mapTime}
	h.SetSpaceTiming(clock.timing())
	dir := &reviewDirectory{reply: signed}
	h.SetSpaces(hub.SpaceConfig{Maps: func(string, crypto.Signer) (space.MapDirectory, error) { return dir, nil }})
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	defer func() { cancel(); <-h.Done() }()
	r := h.SubmitOperator(ctx, 0, protocol.Message{Type: protocol.TypeOperatorIdentity, MessageID: "test-identity", IdentityOp: protocol.IdentityOpStatus})
	spacePinCheck(t, r.Type == protocol.TypeOperatorIdentityResult, "identity status: %v", r)
	err := h.RefreshSpace(ctx, mapSpace)
	spacePinCheck(t, err == nil, "initial refresh: %v", err)

	denied := &space.Refusal{Status: 403}
	dir.err = denied
	err = h.RefreshSpace(ctx, mapSpace)
	spacePinCheck(t, errors.Is(err, denied), "expected refusal, got %v", err)
	spacePinCheck(t, h.SpaceRefreshError(mapSpace) != "", "refusal missing from status")
	got, active := h.SpaceAuthority(mapSpace)
	spacePinCheck(t, active, "lost valid cached authority")
	spacePinCheck(t, got.Mark == signed.Mark(), "cached mark changed on refusal")

	dir.err = nil
	m.Version++
	dir.reply = pairbook.SignSpaceMap(key, m)
	err = h.RefreshSpace(ctx, mapSpace)
	spacePinCheck(t, err == nil, "refresh after refusal: %v", err)
	spacePinCheck(t, h.SpaceRefreshError(mapSpace) == "", "refusal persisted after successful refresh")
	checkHeldMap(t, s, dir.reply)
}
