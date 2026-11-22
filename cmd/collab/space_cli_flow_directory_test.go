//go:build linux

package main

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"encoding/hex"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
)

// A directory shared by isolated owner and member brokers. Each client
// reads maps as its own workspace; pending members receive a 403.
type flowDirectory struct {
	mu        sync.Mutex
	current   pairbook.SignedSpaceMap
	invite    pairbook.SignedMapInvite
	pending   []pairbook.SignedChange
	addresses map[string]pairbook.SignedAddress
	winner    *pairbook.SignedSpaceMap
	conflicts int
}
type flowClient struct {
	space.MapDirectory
	d   *flowDirectory
	key ed25519.PublicKey
}

func (c flowClient) Map(_ context.Context, p space.Pin) (pairbook.SignedSpaceMap, error) {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	v, err := c.d.current.Verify(p.Space, p.Owner, pairbook.Mark{}, time.Now())
	if err != nil {
		return pairbook.SignedSpaceMap{}, err
	}
	if _, ok := v.BoundTo(pairbook.KeyID(c.key)); !ok {
		return pairbook.SignedSpaceMap{}, &space.Refusal{Status: 403}
	}
	return c.d.current, nil
}
func (c flowClient) PutMap(_ context.Context, p space.Pin, put space.MapPut) error {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	if c.d.winner != nil {
		c.d.current = *c.d.winner
		c.d.winner = nil
		c.d.conflicts++
	}
	if c.d.current.Version != 0 && c.d.current.Mark() != put.Base {
		return &space.Refusal{Status: 409}
	}
	if _, err := put.Map.Verify(p.Space, p.Owner, put.Base, time.Now()); err != nil {
		return err
	}
	c.d.current = put.Map
	for _, id := range put.Applied {
		c.d.drop(id)
	}
	return nil
}
func (d *flowDirectory) drop(id string) {
	for i, ch := range d.pending {
		hash := ch.Digest()
		if hex.EncodeToString(hash[:]) == id {
			d.pending = append(d.pending[:i], d.pending[i+1:]...)
			return
		}
	}
}
func (c flowClient) PostMapInvite(_ context.Context, _ space.Pin, inv pairbook.SignedMapInvite) error {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	c.d.invite = inv
	return nil
}
func (c flowClient) MapInvite(_ context.Context, _ space.Invite) (pairbook.SignedMapInvite, error) {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	return c.d.invite, nil
}
func (c flowClient) SubmitChange(_ context.Context, _ space.Pin, ch pairbook.SignedChange, _ *[32]byte) error {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	c.d.pending = append(c.d.pending, ch)
	return nil
}
func (c flowClient) Changes(_ context.Context, _ space.Pin) ([]pairbook.SignedChange, error) {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	return append([]pairbook.SignedChange(nil), c.d.pending...), nil
}
func (c flowClient) MapAddress(_ context.Context, p space.Pin, key ed25519.PublicKey) (pairbook.SignedAddress, error) {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	a := c.d.addresses[pairbook.KeyID(key)]
	return a, a.Verify(p.Space, time.Now())
}
func (d *flowDirectory) addAddress(p string, signer crypto.Signer, address string) error {
	pub := signer.Public().(ed25519.PublicKey)
	now := time.Now()
	a := pairbook.Address{Space: p, Node: pub, Addrs: []string{address}, IssuedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()}
	sig, err := signer.Sign(nil, a.SigningBytes(), crypto.Hash(0))
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.addresses[pairbook.KeyID(pub)] = pairbook.SignedAddress{Address: a, Sig: sig}
	return nil
}

func (c flowClient) DropChange(_ context.Context, _ space.Pin, digest [32]byte) error {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	c.d.drop(hex.EncodeToString(digest[:]))
	return nil
}
