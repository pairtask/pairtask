package space

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pairtask/pairtask/pairbook"
)

// Relay calls (designs/pairbook/relay.md, wire v1). A relay is named by
// its ID everywhere; its origin comes only from this workspace's own
// Pairbook, never from a record another workspace signed.

// ListenGrant is the grant target that registers this workspace's own slot.
const ListenGrant = "listen"

// ErrNoRelayReach: the Pairbook says node keeps no listen slot in the space.
var ErrNoRelayReach = errors.New("space: the workspace listens on no relay in this space")

// RelayInfo is one relay the Pairbook is configured with.
type RelayInfo struct {
	ID  string `json:"relay_id"`
	URL string `json:"url"`
}

// RelayGrant is a short-lived grant to use one relay: Token is the compact
// JWS the relay checks, never logged or put in a URL.
type RelayGrant struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	RelayID   string `json:"relay_id"`
	URL       string `json:"url"`
}

type grantRequest struct {
	To    string `json:"to"`
	Relay string `json:"relay,omitempty"`
}

type relaysBody struct {
	Relays []RelayInfo `json:"relays"`
}

// Relays lists the relays pin's Pairbook is configured with. An entry that
// isn't a relay ID and an https origin is dropped.
func (c *Client) Relays(ctx context.Context, pin Pin) ([]RelayInfo, error) {
	var got relaysBody
	if err := c.v1(ctx, pin, request{method: http.MethodGet, path: "/v1/relays"}, &got); err != nil {
		return nil, err
	}
	kept := got.Relays[:0]
	for _, r := range got.Relays {
		if r.valid() {
			kept = append(kept, r)
		}
	}
	return kept, nil
}

// Grant asks pin's Pairbook for a grant to use relay: to is ListenGrant or
// a workspace's key ID. An empty relay lets a Pairbook with one relay pick.
func (c *Client) Grant(ctx context.Context, pin Pin, to, relay string) (RelayGrant, error) {
	var got RelayGrant
	err := c.v1(ctx, pin, request{method: http.MethodPost, path: mapPath(pin, "/relay-grants"), body: grantRequest{To: to, Relay: relay}}, &got)
	if err != nil {
		return RelayGrant{}, err
	}
	if !got.usable(relay, c.now()) {
		return RelayGrant{}, ErrUntrusted
	}
	return got, nil
}

// PublishRelayReach signs this workspace's relay reachability record for
// pin's space, listing relays in the order a dialer should try them, valid
// for d, and stores it.
func (c *Client) PublishRelayReach(ctx context.Context, pin Pin, relays []string, d time.Duration) error {
	now := c.now()
	record := pairbook.RelayReach{Version: pairbook.RelayReachVersion, Space: pin.Space, Node: c.self, Relays: relays, IssuedAt: now.UnixMilli(), ExpiresAt: now.Add(d).UnixMilli()}
	sig, err := c.sign(record.SigningBytes())
	if err != nil {
		return err
	}
	signed := pairbook.SignedRelayReach{RelayReach: record, Sig: sig}
	return c.v1(ctx, pin, request{method: http.MethodPut, path: mapPath(pin, "/relay-reach/"+pairbook.KeyID(c.self)), body: signed}, nil)
}

// MapRelayReach fetches node's relay reachability record in pin's space,
// and returns it only if node signed it for this space and it is valid now.
func (c *Client) MapRelayReach(ctx context.Context, pin Pin, node ed25519.PublicKey) (pairbook.SignedRelayReach, error) {
	var got pairbook.SignedRelayReach
	err := c.v1(ctx, pin, request{method: http.MethodGet, path: mapPath(pin, "/relay-reach/"+pairbook.KeyID(node))}, &got)
	if notFound(err) {
		return got, ErrNoRelayReach
	}
	if err != nil {
		return got, err
	}
	if !got.Node.Equal(node) || got.Verify(pin.Space, c.now()) != nil {
		return pairbook.SignedRelayReach{}, ErrUntrusted
	}
	return got, nil
}

// Unpublish deletes this workspace's address record in pin's space: it
// stops hosting there, and peers reach it through a relay from their next
// connect. A record already gone is no error.
func (c *Client) Unpublish(ctx context.Context, pin Pin) error {
	err := c.v1(ctx, pin, request{method: http.MethodDelete, path: mapPath(pin, "/addresses/"+pairbook.KeyID(c.self))}, nil)
	if notFound(err) {
		return nil
	}
	return err
}

// valid: a relay ID and a bare https origin: scheme, host and port only.
func (r RelayInfo) valid() bool {
	return pairbook.ValidRelayID(r.ID) && validOrigin(r.URL)
}

// usable: a token, the relay asked for (any when none was), and an expiry
// in the future no further than the longest grant.
func (g RelayGrant) usable(relay string, now time.Time) bool {
	if g.Token == "" || !(RelayInfo{g.RelayID, g.URL}).valid() {
		return false
	}
	if relay != "" && g.RelayID != relay {
		return false
	}
	left := time.UnixMilli(g.ExpiresAt).Sub(now)
	return left > 0 && left <= MaxGrantLifetime+GrantSkew
}

// Grant bounds (wire v1).
const (
	MaxGrantLifetime = 5 * time.Minute
	GrantSkew        = 30 * time.Second
)

// validOrigin: raw is exactly https://HOST[:PORT], with at most a trailing
// slash: anything else (user, path, query, fragment) changes the canonical
// form and is refused.
func validOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	bare := url.URL{Scheme: "https", Host: u.Host}
	return strings.TrimSuffix(raw, "/") == bare.String()
}

// RelayDirectory is a Pairbook client that also serves relays. A directory
// without them (an older Pairbook, a test fake) simply has no relay path.
type RelayDirectory interface {
	Relays(ctx context.Context, pin Pin) ([]RelayInfo, error)
	PublishRelayReach(ctx context.Context, pin Pin, relays []string, d time.Duration) error
	MapRelayReach(ctx context.Context, pin Pin, node ed25519.PublicKey) (pairbook.SignedRelayReach, error)
	Unpublish(ctx context.Context, pin Pin) error
	RelayEnd(pin Pin, relay string) RelayEnd
}

var _ RelayDirectory = (*Client)(nil)
