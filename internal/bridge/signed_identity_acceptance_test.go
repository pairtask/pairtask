package bridge

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

func bridgeIdentityKey(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte(label))
	return ed25519.NewKeyFromSeed(seed[:])
}

type bridgeSignedFixture struct {
	client  *Client
	remote  net.Conn
	message protocol.Message
}

func newBridgeSignedFixture(t *testing.T) bridgeSignedFixture {
	t.Helper()
	person, device := bridgeIdentityKey("bridge-person"), bridgeIdentityKey("bridge-device")
	workspace := signing.KeyID(bridgeIdentityKey("bridge-workspace").Public().(ed25519.PublicKey))
	personPub := person.Public().(ed25519.PublicKey)
	devicePub := device.Public().(ed25519.PublicKey)
	now := time.Now().UTC()
	cert := signing.Issue(person, signing.Cert{Kind: signing.KindDevice, Subject: "operator-chat", Key: devicePub,
		Workspace: workspace, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)})
	header := signing.Header{MessageID: "signed-to-agent", SenderKind: signing.SenderOperator, SenderID: signing.OperatorID,
		Workspace: workspace, Recipients: []string{"alpha"}, CreatedAt: now, TextHash: signing.TextHash("approve")}
	seal := protocol.EncodeSeal(signing.Sign(device, []signing.Cert{cert}, personPub, header))
	c, remote := pipeClient(t)
	c.protocolVersion = protocol.Version
	c.agentID = "alpha"
	c.roots = []protocol.SigningRoot{{KeyID: signing.KeyID(personPub), Person: true, Workspace: workspace, Approved: true}}
	message := protocol.Message{Type: protocol.TypeMsg, MessageID: header.MessageID, From: protocol.OperatorID,
		Origin: protocol.OriginOperator, To: "alpha", Payload: json.RawMessage(`{"text":"approve"}`), Seal: seal}
	return bridgeSignedFixture{client: c, remote: remote, message: message}
}

func (f bridgeSignedFixture) deliver(t *testing.T) *protocol.Verification {
	t.Helper()
	if err := f.remote.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteFrame(f.remote, f.message); err != nil {
		t.Fatal(err)
	}
	out, err := f.client.Receive(context.Background(), 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("expected one message, got %d", len(out.Messages))
	}
	return out.Messages[0].Verification
}

func requireBridgeState(t *testing.T, verdict *protocol.Verification, want signing.State) {
	t.Helper()
	if verdict == nil {
		t.Fatal("recipient omitted verification")
	}
	if verdict.State != want {
		t.Fatalf("verification state %s, want %s: %s", verdict.State, want, verdict.Reason)
	}
}

func TestSignedIdentityAcceptanceBridgeVerifiesDeliveredOperator(t *testing.T) {
	f := newBridgeSignedFixture(t)
	verified := f.deliver(t)
	requireBridgeState(t, verified, signing.Verified)
	if verified.Signer == nil {
		t.Fatal("verified message has no signer")
	}
	if verified.Signer.Basis != signing.BasisPerson {
		t.Fatalf("recipient signer basis: %+v", verified.Signer)
	}
}

func TestSignedIdentityAcceptanceBridgeRejectsClaimedVerdictAndChangedText(t *testing.T) {
	f := newBridgeSignedFixture(t)
	// A broker or peer may lie in a frame. The bridge must compute a fresh
	// verdict from the seal and the actual delivered text.
	f.message.Payload = json.RawMessage(`{"text":"deny"}`)
	f.message.Verification = &protocol.Verification{State: signing.Verified}
	requireBridgeState(t, f.deliver(t), signing.Invalid)
}

func TestSignedIdentityAcceptanceBridgeDoesNotTrustUnsignedVerdict(t *testing.T) {
	f := newBridgeSignedFixture(t)
	f.message.Seal = ""
	f.message.Verification = &protocol.Verification{State: signing.Verified}
	requireBridgeState(t, f.deliver(t), signing.Unsigned)
}
