package bridge

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

func bridgeIdentityKey(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte(label))
	return ed25519.NewKeyFromSeed(seed[:])
}

func TestSignedIdentityAcceptanceBridgeVerifiesDeliveredOperator(t *testing.T) {
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
	remote.SetWriteDeadline(time.Now().Add(time.Second))
	if err := protocol.WriteFrame(remote, message); err != nil {
		t.Fatal(err)
	}
	out, err := c.Receive(context.Background(), 1, time.Second)
	if err != nil || len(out.Messages) != 1 {
		t.Fatalf("signed delivery: %+v, %v", out, err)
	}
	verified := out.Messages[0].Verification
	if verified == nil || verified.State != signing.Verified || verified.Signer == nil || verified.Signer.Basis != signing.BasisPerson {
		t.Fatalf("recipient lost signed operator authority: %+v", verified)
	}

	// A broker or peer may lie in a frame. The bridge must compute a fresh
	// verdict from the seal and the actual delivered text.
	message.Payload = json.RawMessage(`{"text":"deny"}`)
	message.Verification = &protocol.Verification{State: signing.Verified}
	if err := protocol.WriteFrame(remote, message); err != nil {
		t.Fatal(err)
	}
	out, err = c.Receive(context.Background(), 1, time.Second)
	if err != nil || len(out.Messages) != 1 || out.Messages[0].Verification == nil || out.Messages[0].Verification.State != signing.Invalid {
		t.Fatalf("forged broker verdict or changed text gained authority: %+v, %v", out, err)
	}

	message.Seal = ""
	message.Payload = json.RawMessage(`{"text":"approve"}`)
	if err := protocol.WriteFrame(remote, message); err != nil {
		t.Fatal(err)
	}
	out, err = c.Receive(context.Background(), 1, time.Second)
	if err != nil || len(out.Messages) != 1 || out.Messages[0].Verification == nil || out.Messages[0].Verification.State != signing.Unsigned {
		t.Fatalf("unsigned broker frame inherited claimed verification: %+v, %v", out, err)
	}
}
