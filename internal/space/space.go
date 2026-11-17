// Package space is a node's Pairbook client: invite links, the pins a node
// keeps for the spaces it is in, and signed calls to one directory. It
// returns only records that verify against a pin. It imports nothing of the
// node but the public contract, so the store and the hub can both use it.
package space

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/pairtask/pairtask/pairbook"
)

// MaxResponse bounds a Pairbook response body.
const MaxResponse = 1 << 20

var (
	errNotImplemented = errors.New("space: not implemented")

	// ErrInvite is a malformed or non-canonical invite link.
	ErrInvite = errors.New("space: invalid invite link")
	// ErrPinConflict is a pin for a known space with another owner or directory.
	ErrPinConflict = errors.New("space: space already pinned to another owner or directory")
	// ErrDirectory is a pin or invite for another directory than the client's.
	ErrDirectory = errors.New("space: pin or invite names another directory")
)

// Pin is what a node keeps for a space it created or joined: the owner key
// it trusts and the directory it uses. A pin is never replaced.
type Pin struct {
	Space    string    // 32 lowercase hex
	Owner    string    // owner key ID, sha256:HEX
	Base     string    // directory origin, https://HOST[:PORT]
	JoinedAt time.Time // when pinned
}

// Invite is a parsed invite link:
//
//	https://HOST[:PORT]/v0/spaces/ID#token=B64URL&owner=sha256:HEX
//
// The fragment never leaves the node: the token goes only in the join
// body, the owner only into the pin.
type Invite struct {
	Base  string
	Space string
	Token [32]byte
	Owner string
}

// ParseInvite reads a canonical invite link: https, no userinfo or query,
// a canonical space ID, token and owner key ID.
func ParseInvite(link string) (Invite, error) { return Invite{}, errNotImplemented }

// String is the invite's canonical link.
func (i Invite) String() string { return "" }

// Options are a client's injectable parts.
type Options struct {
	HTTP  *http.Client     // nil = a default client; never modified, redirects are refused regardless
	Now   func() time.Time // nil = time.Now
	Nonce io.Reader        // nil = crypto/rand
}

// Client calls one directory, signing as one node.
type Client struct{}

// NewClient is a client of the directory at base (https://HOST[:PORT]) that
// signs with signer, whose public key must be Ed25519.
func NewClient(base string, signer crypto.Signer, opt Options) (*Client, error) {
	return nil, errNotImplemented
}

// Create stores a new space whose owner is the client's key.
func (c *Client) Create(ctx context.Context, space, name string) (Pin, error) {
	return Pin{}, errNotImplemented
}

// Invite issues a one-time invite valid for d.
func (c *Client) Invite(ctx context.Context, pin Pin, d time.Duration) (Invite, error) {
	return Invite{}, errNotImplemented
}

// Join files the client's join request under inv and returns the pin to keep.
func (c *Client) Join(ctx context.Context, inv Invite, name string) (Pin, error) {
	return Pin{}, errNotImplemented
}

// Requests is the space's pending join requests that verify.
func (c *Client) Requests(ctx context.Context, pin Pin) ([]pairbook.JoinRequest, error) {
	return nil, errNotImplemented
}

// Approve admits node with name for d.
func (c *Client) Approve(ctx context.Context, pin Pin, node ed25519.PublicKey, name string, d time.Duration) error {
	return errNotImplemented
}

// Members is the space's members the pin's owner vouches for now.
func (c *Client) Members(ctx context.Context, pin Pin) ([]pairbook.Member, error) {
	return nil, errNotImplemented
}

// Publish signs and stores the client's addresses for d.
func (c *Client) Publish(ctx context.Context, pin Pin, addrs []string, d time.Duration) error {
	return errNotImplemented
}

// Address is node's current address record, when node is a member the pin
// vouches for and the record verifies.
func (c *Client) Address(ctx context.Context, pin Pin, node ed25519.PublicKey) (pairbook.Address, error) {
	return pairbook.Address{}, errNotImplemented
}

// Revoke removes node; revoking the client's own key is leaving.
func (c *Client) Revoke(ctx context.Context, pin Pin, node ed25519.PublicKey) error {
	return errNotImplemented
}

// Delete erases the space; only its owner can.
func (c *Client) Delete(ctx context.Context, pin Pin) error { return errNotImplemented }
