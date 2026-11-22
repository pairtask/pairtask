//go:build linux

package server

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/personkey"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/spaceowner"
	"github.com/pairtask/pairtask/pairbook"
)

const autoSpaceID = "0123456789abcdef0123456789abcdef"
const autoBase = "https://directory.example"

type autoDirectory struct {
	space.MapDirectory
	addresses map[string]pairbook.SignedAddress
	blocked   string
	entered   chan struct{}
}

func (d *autoDirectory) MapAddress(ctx context.Context, _ space.Pin, key ed25519.PublicKey) (pairbook.SignedAddress, error) {
	id := pairbook.KeyID(key)
	if id == d.blocked {
		select {
		case d.entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return pairbook.SignedAddress{}, ctx.Err()
	}
	return d.addresses[id], nil
}

type autoNode struct {
	*redeemOperatorFixture
	key     string
	binding pairbook.Binding
}
type autoPair struct {
	low, high autoNode
	stalled   autoNode
	current   pairbook.SignedSpaceMap
	keys      *personkey.Keys
	directory *autoDirectory
}

func newAutoPair(t *testing.T) autoPair {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "person")
	keys, err := personkey.Create(dir)
	operatorNoError(t, err)
	now := time.Now()
	_, _, err = personkey.CreateAnchor(dir, false, now.Add(-time.Hour))
	operatorNoError(t, err)
	me, err := spaceowner.Load(keys)
	operatorNoError(t, err)
	d := &autoDirectory{addresses: map[string]pairbook.SignedAddress{}, entered: make(chan struct{}, 1)}
	var nodes []autoNode
	for range 3 {
		nodes = append(nodes, newAutoNode(t, d, me, now))
	}
	slices.SortFunc(nodes, func(a, b autoNode) int { return strings.Compare(a.key, b.key) })
	d.blocked = nodes[2].key
	m, err := spaceowner.NewSpace(me, spaceowner.Genesis{Space: autoSpaceID, Name: "Owner", Own: nodes[0].binding}, now)
	operatorNoError(t, err)
	m.Bindings = append(m.Bindings, nodes[1].binding)
	m = pairbook.SignSpaceMap(keys.PersonKey(), m.SpaceMap)
	p := autoPair{nodes[0], nodes[1], nodes[2], m, keys, d}
	for _, n := range []autoNode{p.high, p.low} {
		keepAutoMap(t, n, m)
	}
	waitAutoContact(t, p.low, p.high, true)
	waitAutoContact(t, p.high, p.low, true)
	return p
}
func newAutoNode(t *testing.T, d *autoDirectory, me spaceowner.Me, now time.Time) autoNode {
	t.Helper()
	cfg := redeemConfig()
	f := newConfiguredRedeemOperator(t, cfg, "", func(h *hub.Hub) {
		h.SetSpaces(hub.SpaceConfig{Maps: func(string, crypto.Signer) (space.MapDirectory, error) { return d, nil }})
	})
	ep, err := f.h.PairingEndpoint()
	operatorNoError(t, err)
	pub := ep.Identity.PublicKey()
	key := ep.Identity.Fingerprint()
	address := pairbook.Address{Space: autoSpaceID, Node: pub, Addrs: []string{relayToPairing(t, f)}, IssuedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()}
	sig, err := ep.Identity.Sign(nil, address.SigningBytes(), crypto.Hash(0))
	operatorNoError(t, err)
	d.addresses[key] = pairbook.SignedAddress{Address: address, Sig: sig}
	acceptance, err := f.h.AcceptBinding(hub.BindingRequest{Lineage: pairbook.LineageHex(me.Lineage), Epoch: 1, Person: me.KeyID(), At: now})
	operatorNoError(t, err)
	b := pairbook.Binding{Claim: me.Claim(pub, 1, now), Acceptance: acceptance.Acceptance, WorkspaceKey: pub}
	err = f.st.PinSpace(context.Background(), space.Pin{Space: autoSpaceID, Owner: me.KeyID(), Base: autoBase, JoinedAt: now})
	operatorNoError(t, err)
	return autoNode{f, key, b}
}
func keepAutoMap(t *testing.T, n autoNode, m pairbook.SignedSpaceMap) {
	t.Helper()
	operatorNoError(t, n.h.KeepSpaceMap(context.Background(), m.Space, m))
}
func waitAutoContact(t *testing.T, n, other autoNode, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := n.st.PeerMessages().Confirmed(context.Background(), other.key)
		operatorNoError(t, err)
		if got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("contact %s confirmation did not become %v", other.key, want)
}
