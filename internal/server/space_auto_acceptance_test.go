//go:build linux

package server

import (
	"testing"
	"time"

	"github.com/pairtask/pairtask/pairbook"
)

func TestSpaceAutoDeliveryRequiresExplicitAgentSharing(t *testing.T) {
	p := newAutoPair(t)
	for _, n := range []autoNode{p.low, p.high} {
		var count int
		operatorNoError(t, n.db.QueryRow("SELECT count(*) FROM peer_grants").Scan(&count))
		operatorEqual(t, "initial agent shares", count, 0)
	}
	low := instantSide{p.low.redeemOperatorFixture, p.high.key}
	high := instantSide{p.high.redeemOperatorFixture, p.low.key}
	remoteReplyRoundTrip(t, []instantSide{low, high})
}
func TestSpaceAutoRevocationDuringBlockedDial(t *testing.T) {
	p := newAutoPair(t)
	m := p.current.SpaceMap
	m.Version++
	m.Bindings = append(m.Bindings, p.stalled.binding)
	keepAutoMap(t, p.low, pairbook.SignSpaceMap(p.keys.PersonKey(), m))
	select {
	case <-p.directory.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("unrelated dial never started")
	}
	m.Version++
	m.Grants = nil
	keepAutoMap(t, p.low, pairbook.SignSpaceMap(p.keys.PersonKey(), m))
	waitAutoContact(t, p.low, p.high, false)
}
