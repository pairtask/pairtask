//go:build linux

package server

import "testing"

func TestMobilityReviewPauseResumeFencesInFlightProbe(t *testing.T) {
	p, g := mobilityGatedPair(t)
	client := mobilityBeginGated(t, p, g)
	pauseServerContact(t, p.joiner, p.inviterKey, true)
	pauseServerContact(t, p.joiner, p.inviterKey, false)
	before := mobilityContact(t, p)
	g.open()
	mobilityAssertNotAdopted(t, client)
	operatorEqual(t, "resumed contact retains its configured destination", mobilityContact(t, p).Destination, before.Destination)
}
