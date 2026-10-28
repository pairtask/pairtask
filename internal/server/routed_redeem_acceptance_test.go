//go:build linux

package server

import (
	"bytes"
	"context"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/egress"
)

type routedResolver struct{ calls atomic.Int32 }

func (r *routedResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.calls.Add(1)
	return []netip.Addr{netip.MustParseAddr("192.168.50.7")}, nil
}

func routedEgressFixture(t *testing.T, address string) (string, *routedResolver, <-chan string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "routed-egress-")
	operatorNoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "egress.sock")
	ln, err := net.Listen("unix", path)
	operatorNoError(t, err)
	resolver := &routedResolver{}
	dialed := make(chan string, 2)
	cfg := egress.Config{Resolver: resolver, LocalAddrs: func() ([]netip.Addr, error) { return nil, nil },
		Dial: func(ctx context.Context, network, destination string) (net.Conn, error) {
			dialed <- destination
			return (&net.Dialer{}).DialContext(ctx, network, address)
		}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- egress.NewForwarder(cfg).Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		ln.Close()
		select {
		case err := <-done:
			operatorNoError(t, err)
		case <-time.After(2 * time.Second):
			t.Error("routed egress did not stop")
		}
	})
	return path, resolver, dialed
}

func TestRoutedRedeemAcceptanceTwoBrokersThroughEgressAndSharedIngress(t *testing.T) {
	alice := newRedeemOperator(t, redeemConfig(), "")
	shared := sharedPairingFixture(t, alice)
	ingress := *alice
	ingress.pairSocket = shared
	address := relayToPairing(t, &ingress)
	egressPath, resolver, dialed := routedEgressFixture(t, address)
	cfg := redeemConfig()
	cfg.Dial = func(ctx context.Context, destination string) (net.Conn, error) {
		return egress.Dial(ctx, egressPath, destination)
	}
	bob := newRedeemOperator(t, cfg, "")
	code := pairingOpen(alice.operatorFixture).Code
	redeemReply(t, redeemCommand(bob, "routed-join", "alice.example:443", code))
	row := awaitRedeemState(t, bob, "routed-join", peer.JoinAwaitingAcceptance)
	operatorEqual(t, "destination retained", row.Destination, "alice.example:443")
	operatorEqual(t, "one resolution", resolver.calls.Load(), int32(1))
	operatorEqual(t, "validated IP dialed", <-dialed, "192.168.50.7:443")
	requests, err := alice.st.Requests(context.Background())
	operatorNoError(t, err)
	operatorEqual(t, "one request", len(requests), 1)
	operatorEqual(t, "explicit acceptance still required", requests[0].State, peer.PairingPending)
	operatorEqual(t, "request has actual Bob key", requests[0].IdentityKey, row.Joiner)
	self := pairingSnapshot(t, pairingCommand(alice.operatorFixture, "status", "")).SelfIdentity
	operatorEqual(t, "actual Alice key authenticated", row.Peer, self)
	outcome, found, err := alice.st.Outcome(context.Background(), row.InviterOperationID)
	operatorNoError(t, err)
	operatorEqual(t, "durable matching receipt", found && bytes.Equal(outcome.TranscriptID, row.TranscriptID), true)
	redeemReply(t, redeemCommand(bob, "routed-join", "alice.example:443", "111111"))
	operatorEqual(t, "replay does not resolve again", resolver.calls.Load(), int32(1))
	for _, f := range []*redeemOperatorFixture{alice, bob} {
		pairingNoCode(t, pairingCommand(f.operatorFixture, "status", ""), code)
		for _, table := range []string{"messages", "durable_inbox", "operator_requests"} {
			operatorEqual(t, "no agent authority or messages", f.count(table), 0)
		}
	}
}
