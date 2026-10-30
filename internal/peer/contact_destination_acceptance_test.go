package peer_test

import (
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestContactDestinationAcceptanceMergePreservesContactAuthority(t *testing.T) {
	at := time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC)
	old := peer.Contact{IdentityKey: "peer", Name: "Known peer", Destination: "old:443", DestinationAt: at, EstablishedAt: at, Joined: true}
	for name, incoming := range map[string]peer.Contact{
		"inviter": {IdentityKey: "peer", Name: "New name", Invited: true, DestinationAt: at.Add(time.Hour)},
		"older":   {IdentityKey: "peer", Destination: "older:443", DestinationAt: at.Add(-time.Hour)},
		"equal":   {IdentityKey: "peer", Destination: "equal:443", DestinationAt: at},
	} {
		t.Run(name, func(t *testing.T) {
			got := peer.MergeContact(old, true, incoming)
			invEqual(t, got.Destination, old.Destination)
			invEqual(t, got.DestinationAt.Equal(old.DestinationAt), true)
			invEqual(t, got.Name, old.Name)
			invEqual(t, got.EstablishedAt.Equal(old.EstablishedAt), true)
			invEqual(t, got.IdentityKey, old.IdentityKey)
			invEqual(t, got.Joined, true)
			invEqual(t, got.Invited, incoming.Invited)
		})
	}
}

func TestContactDestinationAcceptanceLegacyEmptyAddressCanBeFilled(t *testing.T) {
	// Old stores could have a known identity without an address or timestamp.
	legacy := peer.Contact{IdentityKey: "peer", Invited: true}
	got := peer.MergeContact(legacy, true, peer.Contact{IdentityKey: "peer", Destination: "new:443", Joined: true})
	invEqual(t, got.Destination, "new:443")
	invEqual(t, got.Invited, true)
	invEqual(t, got.Joined, true)
}
