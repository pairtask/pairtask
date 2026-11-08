//go:build linux

package server

import "testing"

func TestMobilityAcceptanceFailedDialerRecoversByPinnedCandidate(t *testing.T) {
	p, candidate := mobilityFailedPair(t)
	before := mobilityContact(t, p)
	(instantSide{p.joiner, p.inviterKey}).send(t, "mobility-pending", "queued while the endpoint is unavailable")
	operatorEqual(t, "failed dialer browses", mobilityLease(t, p.joiner)["browse"], any(true))
	result := mobilityCandidate(t, p.joiner, candidate)
	operatorEqual(t, "verified endpoint adopted", result["outcome"], any("adopted"))
	operatorEqual(t, "matched pinned contact", result["contact"], any(p.inviterKey))
	after := mobilityContact(t, p)
	operatorEqual(t, "destination recovered", after.Destination, candidate)
	awaitMessageDelivery(t, p.joiner.st.PeerMessages(), p.inviterKey, "mobility-pending")
	before.Destination, before.DestinationAt = after.Destination, after.DestinationAt
	operatorEqual(t, "other contact fields unchanged", after, before)
}

func TestMobilityAcceptanceWorkingRouteWinsOverAnotherValidEndpoint(t *testing.T) {
	p := newInstantPair(t)
	mobilityLease(t, p.joiner)
	before := mobilityContact(t, p)
	candidate := relayToPairing(t, p.inviter)
	result := mobilityCandidate(t, p.joiner, candidate)
	operatorEqual(t, "working route not replaced", result["outcome"], any("skipped"))
	operatorEqual(t, "configured endpoint retained", mobilityContact(t, p), before)
}

func TestMobilityAcceptanceWrongIdentityDoesNotChangeContact(t *testing.T) {
	p, _ := mobilityFailedPair(t)
	before := mobilityContact(t, p)
	stranger := newRedeemOperator(t, redeemConfig(), "")
	result := mobilityCandidate(t, p.joiner, relayToPairing(t, stranger))
	operatorEqual(t, "wrong pin refused", result["outcome"], any("no_match"))
	operatorEqual(t, "no contact attribution on mismatch", result["contact"], any(nil))
	operatorEqual(t, "contact unchanged", mobilityContact(t, p), before)
}

func TestMobilityAcceptancePausedContactCannotAdoptCandidate(t *testing.T) {
	p, candidate := mobilityFailedPair(t)
	pauseServerContact(t, p.joiner, p.inviterKey, true)
	before := mobilityContact(t, p)
	result := mobilityCandidate(t, p.joiner, candidate)
	operatorEqual(t, "paused target skipped", result["outcome"], any("skipped"))
	operatorEqual(t, "paused target does not browse", mobilityLease(t, p.joiner)["browse"], any(false))
	operatorEqual(t, "paused contact unchanged", mobilityContact(t, p), before)
}
