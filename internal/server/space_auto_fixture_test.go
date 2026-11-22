//go:build linux

package server

import (
	"context"

	"encoding/json"
	"net/http"
	"net/http/httptest"

	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/personkey"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/spaceowner"
	"github.com/pairtask/pairtask/pairbook"
)

const autoSpaceID = "0123456789abcdef0123456789abcdef"

type autoDirectory struct {
	server    *httptest.Server
	mu        sync.Mutex
	addresses map[string]pairbook.SignedAddress
	blocked   string
	entered   chan struct{}
}

func (d *autoDirectory) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key, ok := strings.CutPrefix(r.URL.Path, "/v1/spaces/"+autoSpaceID+"/addresses/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPut {
		var a pairbook.SignedAddress
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil || a.Verify(autoSpaceID, time.Now()) != nil || pairbook.KeyID(a.Node) != key {
			http.Error(w, "invalid record", http.StatusBadRequest)
			return
		}
		d.mu.Lock()
		d.addresses[key] = a
		d.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	d.mu.Lock()
	a, found := d.addresses[key]
	blocked := key == d.blocked
	d.mu.Unlock()
	if blocked {
		select {
		case d.entered <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(a)
}

type autoNode struct {
	*redeemOperatorFixture
	endpoint string
	key      string
	binding  pairbook.Binding
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
	d.server = httptest.NewTLSServer(d)
	t.Cleanup(d.server.Close)
	var nodes []autoNode
	for range 3 {
		nodes = append(nodes, newAutoNode(t, d, me, now))
	}
	slices.SortFunc(nodes, func(a, b autoNode) int { return strings.Compare(a.key, b.key) })
	d.mu.Lock()
	d.blocked = nodes[2].key
	d.mu.Unlock()
	m, err := spaceowner.NewSpace(me, spaceowner.Genesis{Space: autoSpaceID, Name: "Owner", Own: nodes[0].binding}, now)
	operatorNoError(t, err)
	m.Bindings = append(m.Bindings, nodes[1].binding)
	m = pairbook.SignSpaceMap(keys.PersonKey(), m.SpaceMap)
	p := autoPair{nodes[0], nodes[1], nodes[2], m, keys, d}
	for _, n := range []autoNode{p.high, p.low} {
		keepAutoMap(t, n, m)
		req := protocol.SpaceRequest{Op: protocol.SpaceOpMapAddress, Space: autoSpaceID, Addresses: []string{n.endpoint}, ValidFor: time.Hour.Milliseconds()}
		out := n.send(protocol.Message{Type: protocol.TypeOperatorSpace, SpaceRequest: &req, MessageID: "publish-address"})
		if out.SpaceResult == nil {
			t.Fatalf("publish %s: %s %s", n.key, out.Code, out.Detail)
		}
	}
	waitAutoContact(t, p.low, p.high, true)
	waitAutoContact(t, p.high, p.low, true)
	return p
}
func newAutoNode(t *testing.T, d *autoDirectory, me spaceowner.Me, now time.Time) autoNode {
	t.Helper()
	cfg := redeemConfig()
	f := newConfiguredRedeemOperator(t, cfg, "", func(h *hub.Hub) {
		h.SetSpaces(hub.SpaceConfig{HTTP: d.server.Client()})
	})
	ep, err := f.h.PairingEndpoint()
	operatorNoError(t, err)
	pub := ep.Identity.PublicKey()
	key := ep.Identity.Fingerprint()
	acceptance, err := f.h.AcceptBinding(hub.BindingRequest{Lineage: pairbook.LineageHex(me.Lineage), Epoch: 1, Person: me.KeyID(), At: now})
	operatorNoError(t, err)
	b := pairbook.Binding{Claim: me.Claim(pub, 1, now), Acceptance: acceptance.Acceptance, WorkspaceKey: pub}
	err = f.st.PinSpace(context.Background(), space.Pin{Space: autoSpaceID, Owner: me.KeyID(), Base: d.server.URL, JoinedAt: now})
	operatorNoError(t, err)
	return autoNode{redeemOperatorFixture: f, endpoint: relayToPairing(t, f), key: key, binding: b}
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
