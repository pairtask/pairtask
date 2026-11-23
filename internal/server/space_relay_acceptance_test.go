//go:build linux

package server

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/personkey"
	"github.com/pairtask/pairtask/internal/spaceowner"
	"github.com/pairtask/pairtask/pairbook"
)

func TestSpaceRelayPairsTwoNonHostingWorkspacesAndDeliversReply(t *testing.T) {
	relay := newSpaceRelayFixture(t)
	directory := &autoDirectory{server: relay.server}
	dir := filepath.Join(t.TempDir(), "person")
	keys, err := personkey.Create(dir)
	operatorNoError(t, err)
	now := time.Now()
	_, _, err = personkey.CreateAnchor(dir, false, now.Add(-time.Hour))
	operatorNoError(t, err)
	me, err := spaceowner.Load(keys)
	operatorNoError(t, err)
	low := newAutoNode(t, directory, me, now)
	high := newAutoNode(t, directory, me, now)
	if low.key > high.key {
		low, high = high, low
	}
	initial, err := spaceowner.NewSpace(me, spaceowner.Genesis{Space: autoSpaceID, Name: "Owner", Own: low.binding}, now)
	operatorNoError(t, err)
	initial.Bindings = append(initial.Bindings, high.binding)
	// Publish both relay slots before the map grants connectivity.
	grants := initial.Grants
	initial.Grants = nil
	initial = pairbook.SignSpaceMap(keys.PersonKey(), initial.SpaceMap)
	for _, node := range []autoNode{high, low} {
		keepAutoMap(t, node, initial)
		startFixtureRelaySlots(t, node)
	}
	waitRelayRecords(t, relay)
	granted := initial.SpaceMap
	granted.Version++
	granted.Grants = grants
	signed := pairbook.SignSpaceMap(keys.PersonKey(), granted)
	keepAutoMap(t, high, signed)
	keepAutoMap(t, low, signed)
	waitAutoContact(t, low, high, true)
	waitAutoContact(t, high, low, true)
	c, ok, err := low.st.Contact(context.Background(), high.key)
	operatorNoError(t, err)
	if !ok {
		t.Fatal("missing installed contact")
	}
	operatorEqual(t, "relay destination", c.Destination, "pairbook-relay:"+autoSpaceID+"/"+fixtureRelayID+"/"+high.key)
	operatorEqual(t, "matching installed pairing", relayPairingID(t, low, high), relayPairingID(t, high, low))
	remoteReplyRoundTrip(t, []instantSide{{low.redeemOperatorFixture, high.key}, {high.redeemOperatorFixture, low.key}})
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if len(relay.initiators) == 0 {
		t.Fatal("no relay dial")
	}
	for _, key := range relay.initiators {
		operatorEqual(t, "relay initiator", key, low.key)
	}
}

func startFixtureRelaySlots(t *testing.T, node autoNode) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	pairing := NewPairing("", node.h, slog.Default())
	go func() { defer close(done); node.h.ServeRelays(ctx, pairing.ServeConn) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("relay slots did not stop")
		}
	})
}
func waitRelayRecords(t *testing.T, r *spaceRelayFixture) {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		r.mu.Lock()
		n := len(r.reach)
		r.mu.Unlock()
		if n == 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("both workspaces did not publish a relay record")
}

func relayPairingID(t *testing.T, local, remote autoNode) string {
	t.Helper()
	var count int
	var id string
	err := local.db.QueryRow("SELECT count(*),hex(space_pairing_id) FROM pairing_exchanges WHERE identity_key=? AND state='installed'", remote.key).Scan(&count, &id)
	operatorNoError(t, err)
	operatorEqual(t, "one installed pairing", count, 1)
	if id == "" {
		t.Fatal("missing space pairing ID")
	}
	return id
}
