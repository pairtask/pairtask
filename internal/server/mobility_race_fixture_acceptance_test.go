//go:build linux

package server

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

type mobilityGate struct {
	old, current     string
	hold, recovered  atomic.Bool
	entered, release chan struct{}
	once             sync.Once
}

func (g *mobilityGate) dial(ctx context.Context, destination string) (net.Conn, error) {
	dial := &net.Dialer{}
	if destination == g.old && g.recovered.Load() {
		return dial.DialContext(ctx, "tcp", g.current)
	}
	if destination == g.current && g.hold.CompareAndSwap(true, false) {
		close(g.entered)
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return dial.DialContext(ctx, "tcp", destination)
}

func (g *mobilityGate) open() { g.once.Do(func() { close(g.release) }) }

func mobilityGatedPair(t *testing.T) (*instantPair, *mobilityGate) {
	t.Helper()
	inviter := newRedeemOperator(t, redeemConfig(), "")
	inviterEP, err := inviter.h.PairingEndpoint()
	operatorNoError(t, err)
	gate := &mobilityGate{old: "127.0.0.1:1", current: relayToPairing(t, inviter), entered: make(chan struct{}), release: make(chan struct{})}
	cfg := redeemConfig()
	cfg.MessageTick = make(chan time.Time)
	cfg.Dial = gate.dial
	joiner := newRedeemOperator(t, cfg, "")
	t.Cleanup(gate.open)
	joinerEP, err := joiner.h.PairingEndpoint()
	operatorNoError(t, err)
	messageConfirm(inviter, joinerEP.Identity, 1)
	messageJoinedContact(joiner, inviterEP.Identity.Fingerprint(), gate.old)
	pair := &instantPair{inviter: inviter, joiner: joiner, inviterKey: inviterEP.Identity.Fingerprint(), joinerKey: joinerEP.Identity.Fingerprint()}
	joiner.h.SyncMessages(pair.inviterKey)
	mobilityAwait(t, func() bool { return joiner.h.MessageSyncStatus(pair.inviterKey).LastFailure != "" })
	return pair, gate
}

func mobilityAwait(t *testing.T, condition func() bool) {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(until) {
			t.Fatal("mobility fixture state did not settle")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func mobilityBeginGated(t *testing.T, p *instantPair, g *mobilityGate) *sessionProbe {
	t.Helper()
	mobilityLease(t, p.joiner)
	g.hold.Store(true)
	client := &sessionProbe{operatorPeer: operatorDial(t, p.joiner.operator)}
	client.send(map[string]any{"type": protocol.TypeOperatorContact, "contact_op": "candidates", "message_id": "gated-candidate", "contact_candidates": []string{g.current}})
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("candidate probe did not reach the dial boundary")
	}
	return client
}

func mobilityAssertNotAdopted(t *testing.T, client *sessionProbe) {
	t.Helper()
	response := client.frame()
	operatorEqual(t, "candidate result completes", response.text("type"), protocol.TypeOperatorContactResult)
	var result []struct {
		Outcome string `json:"outcome"`
	}
	operatorNoError(t, json.Unmarshal(response["contact_candidates_result"], &result))
	operatorEqual(t, "one refused stale candidate", len(result), 1)
	operatorEqual(t, "stale candidate not adopted", result[0].Outcome == "adopted", false)
}

// Simulate a separately verified newer write while the automatic worker
// holds its old snapshot. This tests the storage fence without requiring
// the interactive address UI to preempt its shared probe reservation.
func mobilityCommitNewer(t *testing.T, p *instantPair, candidate string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	before := mobilityContact(t, p)
	ep, err := p.joiner.h.PairingEndpoint()
	operatorNoError(t, err)
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", candidate)
	operatorNoError(t, err)
	defer raw.Close()
	checked, err := firstcontact.MessageClient(ctx, raw, ep.Identity, time.Now, p.inviterKey)
	operatorNoError(t, err)
	defer checked.Close()
	_, _, err = p.joiner.st.BeginContactAddress(ctx, store.AddressOp{ID: "newer-verified", Contact: p.inviterKey, Candidate: candidate, At: time.Now(), PrincipalUID: os.Getuid()})
	operatorNoError(t, err)
	_, err = p.joiner.st.ChangeContactAddress(ctx, store.ContactAddress{OpID: "newer-verified", Contact: p.inviterKey, From: before.Destination, FromAt: before.DestinationAt, To: candidate, At: time.Now(), StillDials: func(c peer.Contact) bool { return c.Joined }})
	operatorNoError(t, err)
}
