//go:build linux

package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
)

func mobilityRequest(t *testing.T, f *redeemOperatorFixture, fields map[string]any) sessionWire {
	t.Helper()
	p := &sessionProbe{operatorPeer: operatorDial(t, f.operator)}
	fields["type"], fields["message_id"] = protocol.TypeOperatorContact, "mobility-request"
	p.send(fields)
	return p.frame()
}

func mobilityLease(t *testing.T, f *redeemOperatorFixture) map[string]any {
	t.Helper()
	response := mobilityRequest(t, f, map[string]any{"contact_op": "discovery"})
	operatorEqual(t, "lease supported", response.text("type"), protocol.TypeOperatorContactResult)
	var lease map[string]any
	operatorNoError(t, json.Unmarshal(response["contact_discovery"], &lease))
	return lease
}

func mobilityCandidate(t *testing.T, f *redeemOperatorFixture, candidate string) map[string]any {
	t.Helper()
	response := mobilityRequest(t, f, map[string]any{"contact_op": "candidates", "contact_candidates": []string{candidate}})
	operatorEqual(t, "candidate op supported", response.text("type"), protocol.TypeOperatorContactResult)
	var results []map[string]any
	operatorNoError(t, json.Unmarshal(response["contact_candidates_result"], &results))
	operatorEqual(t, "one candidate result", len(results), 1)
	operatorEqual(t, "candidate identity retained", results[0]["candidate"], any(candidate))
	return results[0]
}

func mobilityContact(t *testing.T, p *instantPair) peer.Contact {
	t.Helper()
	c, found, err := p.joiner.st.Contact(context.Background(), p.inviterKey)
	operatorNoError(t, err)
	operatorEqual(t, "contact still confirmed", found, true)
	return c
}

func mobilityFailedPair(t *testing.T) (*instantPair, string) {
	t.Helper()
	p := newInstantPair(t)
	// Require feature support before fault injection, so the baseline fails
	// for the missing lease operation, not a fixture timing assumption.
	mobilityLease(t, p.joiner)
	candidate := mobilityContact(t, p).Destination
	_, err := p.joiner.db.Exec(`UPDATE peer_contacts SET destination='127.0.0.1:1' WHERE identity_key=?`, p.inviterKey)
	operatorNoError(t, err)
	p.mu.Lock()
	err = p.link.Close()
	p.mu.Unlock()
	operatorNoError(t, err)
	p.joiner.h.SyncMessages(p.inviterKey)
	until := time.Now().Add(5 * time.Second)
	for p.joiner.h.MessageSyncStatus(p.inviterKey).LastFailure == "" {
		if time.Now().After(until) {
			t.Fatal("fixture failed route did not become visible")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return p, candidate
}
