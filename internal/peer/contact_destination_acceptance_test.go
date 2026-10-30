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
			if got.Destination != old.Destination || !got.DestinationAt.Equal(old.DestinationAt) || got.Name != old.Name || !got.EstablishedAt.Equal(old.EstablishedAt) || got.IdentityKey != old.IdentityKey || !got.Joined {
				t.Fatalf("incoming contact changed established authority: %+v", got)
			}
			if incoming.Invited && !got.Invited {
				t.Fatal("reciprocal pairing direction was lost")
			}
		})
	}
	// Old stores could have a known identity without an address or timestamp.
	legacy := peer.Contact{IdentityKey: "peer", Invited: true}
	got := peer.MergeContact(legacy, true, peer.Contact{IdentityKey: "peer", Destination: "new:443", Joined: true})
	if got.Destination != "new:443" || !got.Invited || !got.Joined {
		t.Fatalf("legacy empty destination was not filled: %+v", got)
	}
}
