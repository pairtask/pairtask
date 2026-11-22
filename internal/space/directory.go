package space

import (
	"context"
	"crypto/ed25519"
	"time"

	"github.com/pairtask/pairtask/pairbook"
)

// Directory is what a node asks of one Pairbook directory; *Client is the
// real one, and tests may stand in for it.
type Directory interface {
	Create(ctx context.Context, id, name string) (Pin, error)
	Invite(ctx context.Context, pin Pin, d time.Duration) (Invite, error)
	Join(ctx context.Context, inv Invite, name string) (Pin, error)
	Requests(ctx context.Context, pin Pin) ([]pairbook.JoinRequest, error)
	Approve(ctx context.Context, pin Pin, req pairbook.JoinRequest, d time.Duration) error
	Members(ctx context.Context, pin Pin) ([]pairbook.Member, error)
	Publish(ctx context.Context, pin Pin, addrs []string, d time.Duration) error
	Address(ctx context.Context, pin Pin, node ed25519.PublicKey) (pairbook.Address, error)
	Revoke(ctx context.Context, pin Pin, node ed25519.PublicKey) error
	Delete(ctx context.Context, pin Pin) error
}

var _ Directory = (*Client)(nil)

// MapDirectory is the Pairbook v1 calls (space maps).
type MapDirectory interface {
	PutMap(ctx context.Context, pin Pin, put MapPut) error
	Map(ctx context.Context, pin Pin) (pairbook.SignedSpaceMap, error)
	PostMapInvite(ctx context.Context, pin Pin, inv pairbook.SignedMapInvite) error
	MapInvite(ctx context.Context, inv Invite) (pairbook.SignedMapInvite, error)
	SubmitChange(ctx context.Context, pin Pin, ch pairbook.SignedChange, token *[32]byte) error
	Changes(ctx context.Context, pin Pin) ([]pairbook.SignedChange, error)
	DropChange(ctx context.Context, pin Pin, digest [32]byte) error
	MapAddress(ctx context.Context, pin Pin, node ed25519.PublicKey) (pairbook.SignedAddress, error)
	PublishAddress(ctx context.Context, pin Pin, addrs []string, d time.Duration) error
}

var _ MapDirectory = (*Client)(nil)
