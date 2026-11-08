//go:build linux

package server

import (
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestMobilityAcceptancePauseFencesInFlightProbe(t *testing.T) {
	p, g := mobilityGatedPair(t)
	client := mobilityBeginGated(t, p, g)
	pauseServerContact(t, p.joiner, p.inviterKey, true)
	before := mobilityContact(t, p)
	g.open()
	mobilityAssertNotAdopted(t, client)
	operatorEqual(t, "pause survives late probe", mobilityContact(t, p), before)
}

func TestMobilityAcceptanceNewerVerifiedAddressFencesInFlightProbe(t *testing.T) {
	p, g := mobilityGatedPair(t)
	client := mobilityBeginGated(t, p, g)
	newer := relayToPairing(t, p.inviter)
	mobilityCommitNewer(t, p, newer)
	g.open()
	mobilityAssertNotAdopted(t, client)
	operatorEqual(t, "newer route retained", mobilityContact(t, p).Destination, newer)
}

func TestMobilityAcceptanceRecoveredOriginalRouteWinsOverInFlightProbe(t *testing.T) {
	p, g := mobilityGatedPair(t)
	client := mobilityBeginGated(t, p, g)
	before := time.Now()
	g.recovered.Store(true)
	p.joiner.h.SyncMessages(p.inviterKey)
	mobilityAwait(t, func() bool { return p.joiner.h.MessageSyncStatus(p.inviterKey).LastSuccess.After(before) })
	g.open()
	mobilityAssertNotAdopted(t, client)
	operatorEqual(t, "recovered configured route retained", mobilityContact(t, p).Destination, g.old)
}

func TestMobilityFixtureControlExistingManualAddressUsesRealPinnedPair(t *testing.T) {
	p, g := mobilityGatedPair(t)
	response := p.joiner.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: "address", ContactKey: p.inviterKey, ContactText: g.current, MessageID: "manual-control"})
	operatorEqual(t, "existing pinned address check works", response.Type, protocol.TypeOperatorContactResult)
	operatorEqual(t, "manual destination updated", mobilityContact(t, p).Destination, g.current)
}
