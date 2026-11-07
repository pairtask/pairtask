package pake_test

import (
	"errors"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestPairingModeAcceptanceMismatchCannotPersistEitherWay(t *testing.T) {
	for _, side := range []string{"joiner", "inviter"} {
		t.Run(side, func(t *testing.T) {
			c := newCase()
			if side == "joiner" {
				c.joinBinding.Mode = peer.ModeEpoch
			} else {
				c.inviteBinding.Mode = peer.ModeEpoch
			}
			result := exchange(t, c)
			requireFailure(t, result.joinErr)
			requireFailure(t, result.inviteErr)
			requireEqual(t, result.persistCalls, 0)
			requireEqual(t, result.inviteResult.Peer, "")
		})
	}
}

func TestPairingModeAcceptanceEpochConfirmationCarriesVerifiedMode(t *testing.T) {
	c := newCase()
	c.joinBinding.Mode, c.inviteBinding.Mode = peer.ModeEpoch, peer.ModeEpoch
	result := exchange(t, c)
	requireOK(t, result.joinErr)
	requireOK(t, result.inviteErr)
	requireEqual(t, result.persistCalls, 1)
	requireEqual(t, result.inviteResult.Mode, peer.ModeEpoch)
}

func TestPairingModeAcceptanceLegacyAndEmptyRemainCompatible(t *testing.T) {
	c := newCase()
	c.joinBinding.Mode = peer.ModeLegacy
	result := exchange(t, c)
	requireOK(t, result.joinErr)
	requireOK(t, result.inviteErr)
	requireEqual(t, result.persistCalls, 1)
}

func TestPairingModeAcceptanceUnknownModeIsInvalidBinding(t *testing.T) {
	b := binding()
	b.Mode = peer.ExchangeMode("unknown-mode")
	requireEqual(t, errors.Is(b.Validate(), peer.ErrInvalidBinding), true)
}
