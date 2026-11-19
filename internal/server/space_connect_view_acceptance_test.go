//go:build linux

package server

import (
	"bytes"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestSpaceConnectionReviewExistingPausedContactKeepsViewAndGeneration(t *testing.T) {
	p := newConnectPair(t)
	key := p.b.identity.Fingerprint()
	requireConnected(t, p.a.connect(key))
	out := p.a.h.SubmitOperator(p.a.ctx, 0, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpPause, ContactKey: key, MessageID: "pause-space-contact"})
	operatorEqual(t, "pause", out.Type, protocol.TypeOperatorContactResult)
	before, found, err := p.a.st.Contact(p.a.ctx, key)
	operatorNoError(t, err)
	operatorEqual(t, "stored contact", found, true)
	got := requireConnected(t, p.a.connect(key))
	operatorEqual(t, "paused state retained", got.PausedAt != nil, true)
	operatorEqual(t, "dialing direction retained", got.Dials, true)
	after, found, err := p.a.st.Contact(p.a.ctx, key)
	operatorNoError(t, err)
	operatorEqual(t, "stored contact", found, true)
	operatorEqual(t, "generation unchanged", after.Generation, before.Generation)
	operatorEqual(t, "still paused", after.PausedAt.Equal(before.PausedAt), true)
}

func TestSpaceConnectionReviewExistingLegacyContactRemainsLabeledLegacy(t *testing.T) {
	p := newConnectPair(t)
	key := p.b.identity.Fingerprint()
	now := time.Now()
	outcome := peer.PairingOutcome{OperationID: "legacy-code-pairing", InvitationID: "legacy-invitation", IdentityKey: key, Name: "Existing Bob", TranscriptID: bytes.Repeat([]byte{7}, 64), At: now}
	_, err := p.a.st.Commit(p.a.ctx, outcome)
	operatorNoError(t, err)
	_, err = p.a.st.Accept(p.a.ctx, key)
	operatorNoError(t, err)
	_, err = p.a.st.Deliver(p.a.ctx, key, peer.PairingAccepted, now)
	operatorNoError(t, err)
	beforeEpoch, _ := p.a.pairing(t, key)
	got := requireConnected(t, p.a.connect(key))
	operatorEqual(t, "legacy status retained", got.Legacy, true)
	operatorEqual(t, "legacy epoch retained", got.PairingEpoch, beforeEpoch)
	operatorEqual(t, "existing name retained", got.Name, "Existing Bob")
}
