package space

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/pairtask/pairtask/pairbook"
)

// Pairbook v1 calls (space maps; designs agreed with the directory's v1
// API). The client sends and fetches records; it trusts none of them
// itself: maps are verified and kept by the store against the pin and the
// durable mark, an invite against the pinned owner here.

const mapsPath = "/v1/spaces/"

// MapPut is a map for the directory to store: the mark it replaces (the
// zero Mark for a space's first map), the map, and the digests of the
// pending changes it applies.
type MapPut struct {
	Base    pairbook.Mark           `json:"base"`
	Map     pairbook.SignedSpaceMap `json:"map"`
	Applied []string                `json:"applied"`
}

// changeRequest is a join and the invite's secret, which proves the joiner
// holds the link.
type changeRequest struct {
	Change pairbook.SignedChange `json:"change"`
	Token  string                `json:"token,omitempty"`
}

type changesBody struct {
	Changes []pairbook.SignedChange `json:"changes"`
}

// PutMap stores put's map in pin's space. A 409 Refusal means another map
// got there first: fetch again and redo the fold.
func (c *Client) PutMap(ctx context.Context, pin Pin, put MapPut) error {
	if put.Applied == nil {
		put.Applied = []string{}
	}
	return c.v1(ctx, pin, request{method: http.MethodPut, path: mapPath(pin, "/map"), body: put}, nil)
}

// Map fetches pin's space's current map, unverified: the store verifies it.
func (c *Client) Map(ctx context.Context, pin Pin) (pairbook.SignedSpaceMap, error) {
	var m pairbook.SignedSpaceMap
	err := c.v1(ctx, pin, request{method: http.MethodGet, path: mapPath(pin, "/map")}, &m)
	return m, err
}

// PostMapInvite stores an owner-signed invite.
func (c *Client) PostMapInvite(ctx context.Context, pin Pin, inv pairbook.SignedMapInvite) error {
	return c.v1(ctx, pin, request{method: http.MethodPost, path: mapPath(pin, "/invites"), body: inv}, nil)
}

// MapInvite fetches the invite a link names, by its token's hash, and
// returns it only if the owner the link pins signed it for this space and
// it is valid now.
func (c *Client) MapInvite(ctx context.Context, inv Invite) (pairbook.SignedMapInvite, error) {
	pin := Pin{Space: inv.Space, Owner: inv.Owner, Base: inv.Base}
	hash := sha256.Sum256(inv.Token[:])
	var got pairbook.SignedMapInvite
	err := c.v1(ctx, pin, request{method: http.MethodGet, path: mapPath(pin, "/invites/"+hex.EncodeToString(hash[:]))}, &got)
	if err != nil {
		return got, err
	}
	if got.TokenHash != hash || got.Verify(inv.Space, inv.Owner, c.now()) != nil {
		return pairbook.SignedMapInvite{}, ErrUntrusted
	}
	return got, nil
}

// ErrChangeToken: a join without the token its TokenHash names, or a
// token on any other request.
var ErrChangeToken = errors.New("space: a join carries its invite token, and only a join")

// SubmitChange sends a person's signed request. A join carries the
// invite's token (proof of the link); other requests pass nil.
func (c *Client) SubmitChange(ctx context.Context, pin Pin, ch pairbook.SignedChange, token *[32]byte) error {
	body, err := changeBody(ch, token)
	if err != nil {
		return err
	}
	return c.v1(ctx, pin, request{method: http.MethodPost, path: mapPath(pin, "/changes"), body: body}, nil)
}

// changeBody is what a request sends, checked before anything is signed.
func changeBody(ch pairbook.SignedChange, token *[32]byte) (changeRequest, error) {
	join := ch.Action == pairbook.ChangeJoin
	if join != (token != nil) {
		return changeRequest{}, ErrChangeToken
	}
	body := changeRequest{Change: ch}
	if join {
		if sha256.Sum256(token[:]) != ch.TokenHash {
			return changeRequest{}, ErrChangeToken
		}
		body.Token = base64.RawURLEncoding.EncodeToString(token[:])
	}
	return body, nil
}

// Changes are the pending requests the owner has to fold, oldest first.
func (c *Client) Changes(ctx context.Context, pin Pin) ([]pairbook.SignedChange, error) {
	var out changesBody
	err := c.v1(ctx, pin, request{method: http.MethodGet, path: mapPath(pin, "/changes")}, &out)
	return out.Changes, err
}

// DropChange tells the directory the owner refused a pending request.
func (c *Client) DropChange(ctx context.Context, pin Pin, digest [32]byte) error {
	return c.v1(ctx, pin, request{method: http.MethodDelete, path: mapPath(pin, "/changes/"+hex.EncodeToString(digest[:]))}, nil)
}

// v1 sends r to pin's directory, refusing a pin for another one.
func (c *Client) v1(ctx context.Context, pin Pin, r request, out any) error {
	if err := c.bound(pin.Base); err != nil {
		return err
	}
	return c.call(ctx, r, out)
}

func mapPath(pin Pin, suffix string) string { return mapsPath + pin.Space + suffix }
