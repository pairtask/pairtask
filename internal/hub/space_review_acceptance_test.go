package hub

import (
	"context"
	"crypto/ed25519"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
)

func TestHubSpaceAcceptanceConcurrentLeaveCannotEraseNewPin(t *testing.T) {
	h := newTestHub(t)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f := &fakeSpaceDirectory{revoke: func(ctx context.Context, _ ed25519.PublicKey) error {
		if calls.Add(1) != 1 {
			return nil
		}
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	ctx := startSpaceHub(t, h, f)
	saveHubPin(t, h, hubPin(pairbook.KeyID(hubSpacePub(hubSpaceKey("old-owner")))))
	first := make(chan protocol.Message, 1)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	go func() {
		first <- spaceOp(h, ctx, protocol.SpaceRequest{Op: protocol.SpaceOpLeave, Space: hubSpaceID})
		close(first)
	}()
	addressAwait(t, started)
	defer func() { unblock(); addressAwait(t, first) }()
	second := spaceOp(h, ctx, protocol.SpaceRequest{Op: protocol.SpaceOpLeave, Space: hubSpaceID})
	// Refusing the overlap preserves ordering. If it ran, reproduce the late
	// first completion erasing a newer join, and require that new pin survives.
	if second.Type == protocol.TypeError {
		requireHubPin(t, h, hubSpaceID, true)
		return
	}
	spaceSuccess(t, second)
	inv := space.Invite{Base: hubDirectory, Space: hubSpaceID, Owner: pairbook.KeyID(hubSpacePub(hubSpaceKey("new-owner")))}
	spaceSuccess(t, spaceOp(h, ctx, protocol.SpaceRequest{Op: protocol.SpaceOpJoin, Link: inv.String()}))
	unblock()
	addressAwait(t, first)
	held, ok, err := h.store.SpacePin(ctx, hubSpaceID)
	if err != nil || !ok || held.Owner != inv.Owner {
		t.Fatal("late leave erased the newer joined pin")
	}
}

func TestHubSpaceAcceptanceDurationBoundsBeforeNetwork(t *testing.T) {
	h := newTestHub(t)
	f := &fakeSpaceDirectory{}
	ctx := startSpaceHub(t, h, f)
	saveHubPin(t, h, hubPin(h.identity.Fingerprint()))
	// Invalid bounds must be classified before directory I/O.
	for _, r := range []protocol.SpaceRequest{
		{Op: protocol.SpaceOpInvite, ValidFor: pairbook.InviteValidity.Milliseconds() + 1},
		{Op: protocol.SpaceOpPublish, Addresses: []string{"host:7"}, ValidFor: pairbook.AddressValidity.Milliseconds() + 1},
		{Op: protocol.SpaceOpApprove, Node: pairbook.KeyID(hubSpacePub(hubSpaceKey("node"))), ValidFor: pairbook.MemberValidity.Milliseconds() + 1},
		{Op: protocol.SpaceOpInvite, ValidFor: 1<<63 - 1},
	} {
		r.Space = hubSpaceID
		out := spaceOp(h, ctx, r)
		spaceRefused(t, out)
		if out.Code != protocol.ErrMalformedFrame {
			t.Fatalf("invalid duration not rejected before directory I/O: %s %s", out.Code, out.Detail)
		}
	}
}
