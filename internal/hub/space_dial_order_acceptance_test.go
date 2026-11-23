package hub

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
)

type dialOrderDirectory struct {
	space.MapDirectory
	byKey   map[string]error
	lookup  error
	queries []string
}

func (d *dialOrderDirectory) MapAddress(_ context.Context, pin space.Pin, key ed25519.PublicKey) (pairbook.SignedAddress, error) {
	d.queries = append(d.queries, pin.Space+"/"+pairbook.KeyID(key))
	if d.byKey != nil {
		return pairbook.SignedAddress{}, d.byKey[pairbook.KeyID(key)]
	}
	return pairbook.SignedAddress{}, d.lookup
}

func TestGrantedDialerOrder(t *testing.T) {
	h := newTestHub(t)
	t.Cleanup(h.authority.close)
	ctx := context.Background()
	if why := h.loadIdentity(ctx); why != "" {
		t.Fatal(why)
	}
	d := new(dialOrderDirectory)
	h.SetSpaces(SpaceConfig{Maps: func(string, crypto.Signer) (space.MapDirectory, error) { return d, nil }})
	m, a, b := retireGrantFixture()
	low, high := min(a, b), max(a, b)
	pin := space.Pin{Space: m.Space, Owner: pairbook.KeyID(m.Owner), Base: hubDirectory, JoinedAt: time.Now()}
	messageWorkerOK(t, h.store.PinSpace(ctx, pin))
	messageWorkerOK(t, h.KeepSpaceMap(ctx, pin.Space, m))
	responder := spaceResponder{h: h}
	for _, tc := range []struct {
		name, from, to string
		lookup, want   error
		queried        bool
	}{
		{"lower key connects without lookup", low, high, context.DeadlineExceeded, nil, false},
		{"higher Desktop with no address", high, low, space.ErrNoAddress, nil, true},
		{"higher Box with published address", high, low, nil, errOtherDials, true},
		{"directory unavailable", high, low, context.DeadlineExceeded, errNotSettled, true},
		{"untrusted address", high, low, space.ErrUntrusted, errNotSettled, true},
		{"self", low, low, nil, errSelfDial, false},
		{"ungranted higher key", "unbound", low, space.ErrNoAddress, errNotGranted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d.lookup = tc.lookup
			d.queries = nil
			err := responder.dialerMayDial(ctx, tc.from, tc.to, pin.Space)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			var want []string
			if tc.queried {
				want = []string{pin.Space + "/" + tc.from}
			}
			if !slices.Equal(d.queries, want) {
				t.Fatalf("queried %v, want %v", d.queries, want)
			}

		})
	}
}

type dialOrderNode struct {
	responder spaceResponder
	dialer    spaceDialer
}

func newDialOrderPair(t *testing.T) (low, high dialOrderNode) {
	t.Helper()
	ctx := context.Background()
	m, _, _ := retireGrantFixture()
	owner := hubSpaceKey("retire owner")
	person := m.People[0]
	now := time.UnixMilli(m.IssuedAt)
	a, first := newDialOrderNode(t, owner, person, now)
	b, second := newDialOrderNode(t, owner, person, now)
	m.Bindings = []pairbook.Binding{first, second}
	a.dialer.target = b.dialer.id.Fingerprint()
	b.dialer.target = a.dialer.id.Fingerprint()
	signed := pairbook.SignSpaceMap(owner, m.SpaceMap)
	pin := space.Pin{Space: m.Space, Owner: pairbook.KeyID(signed.Owner), Base: hubDirectory, JoinedAt: now}
	for _, n := range []dialOrderNode{a, b} {
		messageWorkerOK(t, n.dialer.h.store.PinSpace(ctx, pin))
		messageWorkerOK(t, n.dialer.h.KeepSpaceMap(ctx, pin.Space, signed))
	}
	if a.dialer.id.Fingerprint() < b.dialer.id.Fingerprint() {
		return a, b
	}
	return b, a
}

func requireMatchingPairing(t *testing.T, req space.ConnectRequest, a, b dialOrderNode) {
	t.Helper()
	ctx := context.Background()
	want, err := space.PairingID(req.Space, a.dialer.id.Fingerprint(), b.dialer.id.Fingerprint(), req.ConnectID)
	messageWorkerOK(t, err)
	for _, n := range []dialOrderNode{a, b} {
		op := peer.SpaceOperationPrefix + req.Space + ":" + hex.EncodeToString(req.ConnectID[:])
		x, ok, err := n.dialer.h.store.Exchange(ctx, op)
		messageWorkerOK(t, err)
		if !ok || x.State != peer.ExchangeInstalled || !bytes.Equal(x.SpacePairingID, want[:]) {
			t.Fatalf("workspace %s has no matching installed exchange", n.dialer.id.Fingerprint())
		}
	}
}

func TestDesktopBoxPairingBothKeyOrders(t *testing.T) {
	for _, higher := range []bool{false, true} {
		t.Run(fmt.Sprintf("desktop-higher-%t", higher), func(t *testing.T) {
			ctx := context.Background()
			desktop, box := newDialOrderPair(t)
			if higher {
				desktop, box = box, desktop
			}
			req := space.ConnectRequest{Space: hubSpaceID, ConnectID: [16]byte{1}}
			messageWorkerOK(t, box.responder.accept(ctx, desktop.dialer.id.Fingerprint(), req))
			messageWorkerOK(t, desktop.dialer.install(ctx, req, "127.0.0.1:8001"))
			requireMatchingPairing(t, req, desktop, box)
		})
	}
}

func TestConcurrentSpaceConnectsConverge(t *testing.T) {
	ctx := context.Background()
	low, high := newDialOrderPair(t)
	fromLow := space.ConnectRequest{Space: hubSpaceID, ConnectID: [16]byte{1}}
	fromHigh := space.ConnectRequest{Space: hubSpaceID, ConnectID: [16]byte{2}}
	// Both responses arrive before either outbound dialer handles its answer.
	messageWorkerOK(t, low.responder.accept(ctx, high.dialer.id.Fingerprint(), fromHigh))
	messageWorkerOK(t, high.responder.accept(ctx, low.dialer.id.Fingerprint(), fromLow))
	messageWorkerOK(t, low.dialer.install(ctx, fromLow, "127.0.0.1:8001"))
	messageWorkerOK(t, high.dialer.install(ctx, fromHigh, "127.0.0.1:8002"))
	requireMatchingPairing(t, fromLow, low, high)
}

func TestPendingSpaceAttemptAfterIncomingInstall(t *testing.T) {
	ctx := context.Background()
	low, high := newDialOrderPair(t)
	target := high.dialer.id.Fingerprint()
	incoming := space.ConnectRequest{Space: hubSpaceID, ConnectID: [16]byte{2}}
	messageWorkerOK(t, low.responder.accept(ctx, target, incoming))
	// A crash after the remote accepts our concurrent dial leaves its ID
	// on disk alongside the incoming contact. Only the outbound ID wins.
	id := [16]byte{1}
	_, _, err := low.dialer.h.store.SpaceAttempt(ctx, target, hubSpaceID, id)
	messageWorkerOK(t, err)
	outgoing := space.ConnectRequest{Space: hubSpaceID, ConnectID: id}
	messageWorkerOK(t, high.responder.accept(ctx, low.dialer.id.Fingerprint(), outgoing))
	skip, err := low.dialer.h.skipDial(ctx, target)
	messageWorkerOK(t, err)
	if skip {
		t.Fatal("reconciliation skipped the persisted outbound attempt")
	}
	messageWorkerOK(t, low.dialer.install(ctx, outgoing, "127.0.0.1:8001"))
	requireMatchingPairing(t, outgoing, low, high)
}

func TestSpaceDialSelectionByReachability(t *testing.T) {
	low, high := newDialOrderPair(t)
	ctx := context.Background()
	d := &dialOrderDirectory{}
	factory := func(string, crypto.Signer) (space.MapDirectory, error) { return d, nil }
	low.dialer.h.SetSpaces(SpaceConfig{Maps: factory})
	high.dialer.h.SetSpaces(SpaceConfig{Maps: factory})
	lowKey, highKey := low.dialer.id.Fingerprint(), high.dialer.id.Fingerprint()
	for _, tc := range []struct {
		name              string
		lowAddr, highAddr error
		fromLow, fromHigh bool
	}{
		{"two Boxes", nil, nil, true, false},
		{"lower Desktop", space.ErrNoAddress, nil, true, false},
		{"higher Desktop", nil, space.ErrNoAddress, false, true},
		{"neither reachable", space.ErrNoAddress, space.ErrNoAddress, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d.byKey = map[string]error{lowKey: tc.lowAddr, highKey: tc.highAddr}
			checkDialChoice(t, ctx, low, high, tc.fromLow)
			checkDialChoice(t, ctx, high, low, tc.fromHigh)
		})
	}
}

func checkDialChoice(t *testing.T, ctx context.Context, from, to dialOrderNode, want bool) {
	t.Helper()
	target := grantedPeer{key: to.dialer.id.PublicKey(), spaces: []string{hubSpaceID}}
	if got := from.dialer.h.dials(ctx, from.dialer.id, to.dialer.id.Fingerprint(), target); got != want {
		t.Errorf("dial decision %v, want %v", got, want)
	}
}

func newDialOrderNode(t *testing.T, owner ed25519.PrivateKey, person pairbook.MapPerson, now time.Time) (dialOrderNode, pairbook.Binding) {
	t.Helper()
	ctx := context.Background()
	h := newTestHub(t)
	t.Cleanup(h.authority.close)
	if why := h.loadIdentity(ctx); why != "" {
		t.Fatal(why)
	}
	a, err := h.AcceptBinding(BindingRequest{Lineage: pairbook.LineageHex(person.Lineage), Epoch: 1, Person: pairbook.KeyID(person.Person), At: now})
	messageWorkerOK(t, err)
	claim := identity.Claim{Lineage: identity.Lineage(person.Lineage), Epoch: 1, Workspace: pairbook.KeyID(a.WorkspaceKey), At: now}.Signed(owner)
	binding := pairbook.Binding{Claim: claim.Marshal(), Acceptance: a.Acceptance, WorkspaceKey: a.WorkspaceKey}
	ep, err := h.PairingEndpoint()
	messageWorkerOK(t, err)
	h.SetSpaces(SpaceConfig{Maps: func(string, crypto.Signer) (space.MapDirectory, error) {
		return &dialOrderDirectory{lookup: space.ErrNoAddress}, nil
	}})
	return dialOrderNode{spaceResponder{h, ep.Identity}, spaceDialer{h: h, id: ep.Identity}}, binding
}
