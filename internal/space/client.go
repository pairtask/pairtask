package space

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/pairtask/pairtask/pairbook"
)

// Options are a client's injectable parts.
type Options struct {
	HTTP  *http.Client     // nil = a default client; never modified, redirects are refused regardless
	Now   func() time.Time // nil = time.Now
	Nonce io.Reader        // nil = crypto/rand
}

// Client calls one directory, signing as one node.
type Client struct {
	base   string
	signer crypto.Signer
	self   ed25519.PublicKey
	http   *http.Client
	now    func() time.Time
	random io.Reader
}

// NewClient is a client of the directory at base (https://HOST[:PORT]) that
// signs with signer, whose public key must be Ed25519.
func NewClient(base string, signer crypto.Signer, opt Options) (*Client, error) {
	if err := checkBase(base); err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("space: no signer")
	}
	self, ok := signer.Public().(ed25519.PublicKey)
	if !ok || len(self) != ed25519.PublicKeySize {
		return nil, errors.New("space: signer key is not Ed25519")
	}
	c := &Client{base: base, signer: signer, self: self, http: noRedirects(opt.HTTP), now: opt.Now, random: opt.Nonce}
	if c.now == nil {
		c.now = time.Now
	}
	if c.random == nil {
		c.random = rand.Reader
	}
	return c, nil
}

// noRedirects is a copy of h that never follows a redirect: a signed
// request goes only where it was signed for. h itself is left alone.
func noRedirects(h *http.Client) *http.Client {
	out := &http.Client{Timeout: 15 * time.Second}
	if h != nil {
		copied := *h
		out = &copied
	}
	out.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return out
}

// Create stores a new space whose owner is the client's key, under id, or a
// random ID when id is "".
func (c *Client) Create(ctx context.Context, id, name string) (Pin, error) {
	id, err := c.spaceID(id)
	if err != nil {
		return Pin{}, err
	}
	now := c.now()
	m, err := c.signMember(pairbook.Member{Space: id, Node: c.self, Name: name, Role: pairbook.RoleOwner, IssuedAt: now.UnixMilli(), ExpiresAt: now.Add(pairbook.MemberValidity).UnixMilli()})
	if err != nil {
		return Pin{}, err
	}
	if err := c.call(ctx, request{method: http.MethodPost, path: "/v0/spaces", body: m}, nil); err != nil {
		return Pin{}, err
	}
	return Pin{Space: id, Owner: pairbook.KeyID(c.self), Base: c.base, JoinedAt: now}, nil
}

func (c *Client) spaceID(id string) (string, error) {
	if id != "" {
		if !validSpaceID(id) {
			return "", fmt.Errorf("space: invalid space ID %q", id)
		}
		return id, nil
	}
	var b [16]byte
	if _, err := io.ReadFull(c.random, b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Invite issues a one-time invite valid for d.
func (c *Client) Invite(ctx context.Context, pin Pin, d time.Duration) (Invite, error) {
	inv := Invite{Base: pin.Base, Space: pin.Space, Owner: pin.Owner}
	if err := c.bound(pin.Base); err != nil {
		return Invite{}, err
	}
	if _, err := io.ReadFull(c.random, inv.Token[:]); err != nil {
		return Invite{}, err
	}
	now := c.now()
	record := pairbook.Invite{Space: pin.Space, TokenHash: sha256.Sum256(inv.Token[:]), IssuedAt: now.UnixMilli(), ExpiresAt: now.Add(d).UnixMilli()}
	sig, err := c.sign(record.SigningBytes())
	if err != nil {
		return Invite{}, err
	}
	signed := pairbook.SignedInvite{Invite: record, Owner: c.self, Sig: sig}
	return inv, c.call(ctx, request{method: http.MethodPost, path: spacePath(pin, "/invites"), body: signed}, nil)
}

// joinBody is a join request and the invite's token: the token proves the
// joiner holds the link, not only the hash the directory keeps.
type joinBody struct {
	Request pairbook.SignedJoinRequest `json:"request"`
	Token   string                     `json:"token"`
}

// Join files the client's join request under inv and returns the pin to
// keep. The invite's owner stays here: it is what the node will trust.
func (c *Client) Join(ctx context.Context, inv Invite, name string) (Pin, error) {
	if err := c.bound(inv.Base); err != nil {
		return Pin{}, err
	}
	now := c.now()
	record := pairbook.JoinRequest{Space: inv.Space, Node: c.self, Name: name, TokenHash: sha256.Sum256(inv.Token[:]), At: now.UnixMilli()}
	sig, err := c.sign(record.SigningBytes())
	if err != nil {
		return Pin{}, err
	}
	body := joinBody{Request: pairbook.SignedJoinRequest{JoinRequest: record, Sig: sig}, Token: b64.EncodeToString(inv.Token[:])}
	if err := c.call(ctx, request{method: http.MethodPost, path: spacesPath + inv.Space + "/requests", body: body}, nil); err != nil {
		return Pin{}, err
	}
	return Pin{Space: inv.Space, Owner: inv.Owner, Base: inv.Base, JoinedAt: now}, nil
}

// Requests is the space's pending join requests that verify. A request
// is checked at its own time, not only within JoinFreshness of now: an
// owner who was offline still sees one up to InviteValidity old.
func (c *Client) Requests(ctx context.Context, pin Pin) ([]pairbook.JoinRequest, error) {
	var out struct {
		Requests []pairbook.SignedJoinRequest `json:"requests"`
	}
	if err := c.get(ctx, pin, "/requests", &out); err != nil {
		return nil, err
	}
	now := c.now()
	var requests []pairbook.JoinRequest
	for _, r := range out.Requests {
		if pending(r, pin.Space, now) {
			requests = append(requests, r.JoinRequest)
		}
	}
	return requests, nil
}

// pending: r verifies for space at its own time, is no older than an
// invite can last, and not from further ahead than JoinFreshness.
func pending(r pairbook.SignedJoinRequest, space string, now time.Time) bool {
	at := time.UnixMilli(r.At)
	return r.Verify(space, at) == nil && now.Sub(at) <= pairbook.InviteValidity && at.Sub(now) <= pairbook.JoinFreshness
}

// Approve admits the node of req, a request the owner saw in pin's space,
// under its name, for d.
func (c *Client) Approve(ctx context.Context, pin Pin, req pairbook.JoinRequest, d time.Duration) error {
	if err := c.bound(pin.Base); err != nil {
		return err
	}
	if req.Space != pin.Space {
		return fmt.Errorf("%w: request is for another space", ErrUntrusted)
	}
	now := c.now()
	m, err := c.signMember(pairbook.Member{Space: pin.Space, Node: req.Node, Name: req.Name, Role: pairbook.RoleMember, IssuedAt: now.UnixMilli(), ExpiresAt: now.Add(d).UnixMilli()})
	if err != nil {
		return err
	}
	return c.call(ctx, request{method: http.MethodPost, path: spacePath(pin, "/members"), body: m}, nil)
}

func (c *Client) signMember(m pairbook.Member) (pairbook.SignedMember, error) {
	sig, err := c.sign(m.SigningBytes())
	return pairbook.SignedMember{Member: m, Owner: c.self, Sig: sig}, err
}

// Members is the space's members the pin's owner vouches for now.
func (c *Client) Members(ctx context.Context, pin Pin) ([]pairbook.Member, error) {
	var roster pairbook.Roster
	if err := c.get(ctx, pin, "/members", &roster); err != nil {
		return nil, err
	}
	return roster.Verified(pin.Space, pin.Owner, c.now()), nil
}

// Publish signs and stores the client's addresses for d.
func (c *Client) Publish(ctx context.Context, pin Pin, addrs []string, d time.Duration) error {
	if err := c.bound(pin.Base); err != nil {
		return err
	}
	now := c.now()
	record := pairbook.Address{Space: pin.Space, Node: c.self, Addrs: slices.Compact(slices.Sorted(slices.Values(addrs))), IssuedAt: now.UnixMilli(), ExpiresAt: now.Add(d).UnixMilli()}
	sig, err := c.sign(record.SigningBytes())
	if err != nil {
		return err
	}
	signed := pairbook.SignedAddress{Address: record, Sig: sig}
	return c.call(ctx, request{method: http.MethodPut, path: spacePath(pin, "/addresses/"+pairbook.KeyID(c.self)), body: signed}, nil)
}

// Address is node's current address record, when node is a member the pin
// vouches for and the record verifies.
func (c *Client) Address(ctx context.Context, pin Pin, node ed25519.PublicKey) (pairbook.Address, error) {
	members, err := c.Members(ctx, pin)
	if err != nil {
		return pairbook.Address{}, err
	}
	if !slices.ContainsFunc(members, func(m pairbook.Member) bool { return node.Equal(m.Node) }) {
		return pairbook.Address{}, fmt.Errorf("%w: not a member", ErrUntrusted)
	}
	var a pairbook.SignedAddress
	if err := c.get(ctx, pin, "/addresses/"+pairbook.KeyID(node), &a); err != nil {
		return pairbook.Address{}, err
	}
	if a.Verify(pin.Space, c.now()) != nil || !node.Equal(a.Node) {
		return pairbook.Address{}, ErrUntrusted
	}
	return a.Address, nil
}

// Revoke removes node; revoking the client's own key is leaving.
func (c *Client) Revoke(ctx context.Context, pin Pin, node ed25519.PublicKey) error {
	if err := c.bound(pin.Base); err != nil {
		return err
	}
	record := pairbook.Revocation{Space: pin.Space, Node: node, By: c.self, At: c.now().UnixMilli()}
	sig, err := c.sign(record.SigningBytes())
	if err != nil {
		return err
	}
	signed := pairbook.SignedRevocation{Revocation: record, Sig: sig}
	return c.call(ctx, request{method: http.MethodPost, path: spacePath(pin, "/revocations"), body: signed}, nil)
}

// Delete erases the space; only its owner can. A directory that answers
// with its own not_found error already erased it, as when a retry follows
// a delete whose answer was lost.
func (c *Client) Delete(ctx context.Context, pin Pin) error {
	if err := c.bound(pin.Base); err != nil {
		return err
	}
	err := c.call(ctx, request{method: http.MethodDelete, path: spacePath(pin, "")}, nil)
	if notFound(err) {
		return nil
	}
	return err
}

// notFound: err is the directory's own 404 not_found answer, not any 404.
func notFound(err error) bool {
	var refused *Refusal
	return errors.As(err, &refused) && refused.Status == http.StatusNotFound && refused.Code == "not_found"
}

func spacePath(pin Pin, suffix string) string { return spacesPath + pin.Space + suffix }

// bound: base is this client's directory, checked before anything is
// signed, so credentials never go to a directory the pin doesn't name.
func (c *Client) bound(base string) error {
	if base != c.base {
		return ErrDirectory
	}
	return nil
}

// sign signs msg; a signer that fails stops the call before any request.
func (c *Client) sign(msg []byte) ([]byte, error) {
	sig, err := c.signer.Sign(nil, msg, crypto.Hash(0))
	if err != nil {
		return nil, fmt.Errorf("space: sign: %w", err)
	}
	return sig, nil
}

// get reads suffix of pin's space into out.
func (c *Client) get(ctx context.Context, pin Pin, suffix string, out any) error {
	if err := c.bound(pin.Base); err != nil {
		return err
	}
	return c.call(ctx, request{method: http.MethodGet, path: spacePath(pin, suffix)}, out)
}

// request is one API call: a nil body sends none.
type request struct {
	method string
	path   string
	body   any
}

// call sends r signed and decodes a 2xx answer into out, when out isn't nil.
func (c *Client) call(ctx context.Context, r request, out any) error {
	req, err := c.signed(ctx, r)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponse+1))
	if err != nil {
		return err
	}
	return answer(resp.StatusCode, data, out)
}

// Refusal is a directory's non-2xx answer: its status and, when the body
// is exactly the API's {"error":CODE}, that code.
type Refusal struct {
	Status int
	Code   string
}

func (r *Refusal) Error() string {
	if r.Code != "" {
		return fmt.Sprintf("space: directory answered %d %s", r.Status, r.Code)
	}
	return fmt.Sprintf("space: directory answered %d", r.Status)
}

// refusal reads a non-2xx answer; a body that isn't exactly one JSON
// object with an error string leaves the code empty.
func refusal(status int, data []byte) *Refusal {
	var body struct {
		Error string `json:"error"`
	}
	r := &Refusal{Status: status}
	if json.Unmarshal(data, &body) == nil {
		r.Code = body.Error
	}
	return r
}

// answer checks a response: a 2xx status, a bounded body, and JSON that
// decodes into out.
func answer(status int, data []byte, out any) error {
	if status < 200 || status > 299 {
		return refusal(status, data)
	}
	if len(data) > MaxResponse {
		return errors.New("space: directory answer too large")
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("space: directory answer: %w", err)
	}
	return nil
}

// signed builds r's HTTP request and its Authorization: the signature
// covers the exact target the request sends and the body bytes.
func (c *Client) signed(ctx context.Context, r request) (*http.Request, error) {
	var body []byte
	if r.body != nil {
		var err error
		if body, err = json.Marshal(r.body); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, r.method, c.base+r.path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	a := pairbook.HTTPAuth{Key: c.self, Method: r.method, Target: req.URL.RequestURI(), BodyHash: sha256.Sum256(body), At: c.now().UnixMilli()}
	if _, err := io.ReadFull(c.random, a.Nonce[:]); err != nil {
		return nil, fmt.Errorf("space: nonce: %w", err)
	}
	if a.Sig, err = c.sign(a.SigningBytes()); err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", a.Header())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}
