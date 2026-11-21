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
	if r.Type != protocol.TypeOperatorIdentityResult {
		t.Fatal(r)
	}
	if err := h.RefreshSpace(ctx, mapSpace); err != nil {
		t.Fatal(err)
	}
	denied := &space.Refusal{Status: 403}
	dir.err = denied
	if err := h.RefreshSpace(ctx, mapSpace); !errors.Is(err, denied) || h.SpaceRefreshError(mapSpace) == "" {
		t.Fatal(err)
	}
	if got, ok := h.SpaceAuthority(mapSpace); !ok || got.Mark != signed.Mark() {
		t.Fatal("lost valid cached authority")
	}
	dir.err = nil
	m.Version++
	dir.reply = pairbook.SignSpaceMap(key, m)
	if err := h.RefreshSpace(ctx, mapSpace); err != nil || h.SpaceRefreshError(mapSpace) != "" {
		t.Fatal(err)
	}
	checkHeldMap(t, s, dir.reply)
}
