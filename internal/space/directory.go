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
