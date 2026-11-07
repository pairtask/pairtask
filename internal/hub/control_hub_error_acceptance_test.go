package hub

import (
	"fmt"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestControlHubErrorAcceptanceStorageFamilyPreservesSpecificCapacityCodes(t *testing.T) {
	bare := pairingError(fmt.Errorf("retained store: %w", peer.ErrIdentityStorage))
	messageSyncEqual(t, bare.code, "identity_storage")
	specific := pairingError(fmt.Errorf("retained request: %w", peer.ErrIdentityCapacity))
	messageSyncEqual(t, specific.code, protocol.ErrPairing)
	messageSyncEqual(t, specific.detail, "identity_capacity")
	messageSyncEqual(t, settleCause(peer.ErrContactCapacity), "contact_capacity")
}
