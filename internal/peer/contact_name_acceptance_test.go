package peer_test

import (
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestContactNameAcceptanceDefaultCanImproveWithoutReplacingLocalAlias(t *testing.T) {
	for _, oldName := range []string{"", "workspace"} {
		t.Run(oldName, func(t *testing.T) {
			old := peer.Contact{IdentityKey: "peer", Name: oldName, LocalName: "My alias", Invited: true}
			got := peer.MergeContact(old, true, peer.Contact{IdentityKey: "peer", Name: "Remote laptop", Invited: true})
			invEqual(t, got.Name, "Remote laptop")
			invEqual(t, got.LocalName, "My alias")
			invEqual(t, got.IdentityKey, old.IdentityKey)
			got = peer.MergeContact(got, true, peer.Contact{IdentityKey: "peer", Name: "Another name"})
			invEqual(t, got.Name, "Remote laptop")
			invEqual(t, got.LocalName, "My alias")
		})
	}
}
