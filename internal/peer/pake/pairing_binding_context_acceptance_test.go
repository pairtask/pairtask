package pake

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func pairingContextSample() peer.ExchangeBinding {
	return peer.ExchangeBinding{InvitationID: "invitation", JoinerOperationID: "join-operation", InviterOperationID: "invite-operation", JoinerName: "Bob"}
}

func TestPairingBindingContextAcceptanceLegacyGoldenBytesStayStable(t *testing.T) {
	b := pairingContextSample()
	old := bindingContext(b)
	digest := sha256.Sum256(old)
	if hex.EncodeToString(digest[:]) != "0d4142c95c3ec1d547667c58a8ebfca0f43c5bafc423e376dc3c00e09ca935e8" {
		t.Fatal("legacy binding context bytes changed")
	}
	b.Mode = peer.ModeLegacy
	if !bytes.Equal(bindingContext(b), old) {
		t.Fatal("explicit legacy mode changed the v8 context")
	}
}

func TestPairingBindingContextAcceptanceEpochAddsOneLengthPrefixedField(t *testing.T) {
	b := pairingContextSample()
	old := bindingContext(b)
	b.Mode = peer.ModeEpoch
	want := append(bytes.Clone(old), 0, 0, 0, 10)
	want = append(want, []byte("mode:epoch")...)
	if !bytes.Equal(bindingContext(b), want) {
		t.Fatal("epoch binding did not append the mode:epoch field to the legacy context")
	}
}
