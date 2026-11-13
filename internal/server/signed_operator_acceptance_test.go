//go:build linux

package server

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

func serverSignedKey(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte(label))
	return ed25519.NewKeyFromSeed(seed[:])
}

func signedOperatorIdentity(t *testing.T, f *operatorFixture, personKey string) protocol.IdentityView {
	t.Helper()
	status := f.send(protocol.Message{Type: protocol.TypeOperatorIdentity, MessageID: "identity-status", IdentityOp: protocol.IdentityOpStatus})
	if status.Type != protocol.TypeOperatorIdentityResult || status.Identity == nil || status.Identity.Workspace == "" {
		t.Fatalf("identity status: %+v", status)
	}
	if personKey != "" {
		registered := f.send(protocol.Message{Type: protocol.TypeOperatorIdentity, MessageID: "identity-register", IdentityOp: protocol.IdentityOpRegister, IdentityKey: personKey})
		if registered.Type != protocol.TypeOperatorIdentityResult || registered.Identity == nil {
			t.Fatalf("person key not registered: %+v", registered)
		}
		return *registered.Identity
	}
	return *status.Identity
}

func signedOperatorPeer(t *testing.T, f *operatorFixture, id string) (*operatorPeer, protocol.Message) {
	t.Helper()
	p := operatorDial(t, f.agent)
	p.write(protocol.Message{Type: "hello", AgentID: id, ProtocolVersion: protocol.Version})
	welcome := p.read()
	if welcome.Type != "welcome" {
		t.Fatalf("agent welcome: %+v", welcome)
	}
	return p, welcome
}

func signedOperatorRequest(id, to, workspace string, person, deviceKey ed25519.PrivateKey, at time.Time) protocol.Message {
	req := operatorRequest(id, to)
	cert := signing.Issue(person, signing.Cert{Kind: signing.KindDevice, Subject: "operator-chat", Key: deviceKey.Public().(ed25519.PublicKey),
		Workspace: workspace, NotBefore: at.Add(-time.Minute), NotAfter: at.Add(time.Hour)})
	h := signing.Header{MessageID: req.MessageID, SenderKind: signing.SenderOperator, SenderID: signing.OperatorID,
		Workspace: workspace, Recipients: protocol.SealRecipients(req.To, req.Group, req.Room), InReplyTo: req.InReplyTo,
		CreatedAt: at, TextHash: signing.TextHash("PRIVATE_OPERATOR_PAYLOAD")}
	req.Seal = protocol.EncodeSeal(signing.Sign(deviceKey, []signing.Cert{cert}, person.Public().(ed25519.PublicKey), h))
	return req
}

func TestSignedIdentityAcceptanceOperatorSealThroughRealBroker(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	person, device := serverSignedKey("server-person"), serverSignedKey("server-device")
	identity := signedOperatorIdentity(t, f, base64.StdEncoding.EncodeToString(person.Public().(ed25519.PublicKey)))
	agent, welcome := signedOperatorPeer(t, f, "alpha")
	if _, ok := protocol.RootsOf(welcome.SigningRoots)(signing.KeyID(person.Public().(ed25519.PublicKey))); !ok {
		t.Fatal("agent welcome omitted the approved person key")
	}
	req := signedOperatorRequest("signed-direct", "alpha", identity.Workspace, person, device, time.Now())
	requireOperatorAccepted(t, f.send(req), "alpha")
	delivered := agent.read()
	if delivered.Seal != req.Seal || delivered.Origin != protocol.OriginOperator {
		t.Fatalf("broker changed signed provenance: %+v", delivered)
	}
	seal, err := protocol.DecodeSeal(delivered.Seal)
	operatorNoError(t, err)
	if verdict := signing.Verify(&seal, protocol.RootsOf(welcome.SigningRoots), time.Now()); verdict.State != signing.Verified || verdict.Signer == nil || verdict.Signer.Basis != signing.BasisPerson {
		t.Fatalf("recipient could not verify operator: %+v", verdict)
	}
	if err := seal.Matches(signing.Frame{MessageID: delivered.MessageID, SenderKind: signing.SenderOperator, SenderID: signing.OperatorID,
		Text: "PRIVATE_OPERATOR_PAYLOAD", Addresses: []string{"alpha"}}); err != nil {
		t.Fatalf("delivered frame differs from signed intent: %v", err)
	}
	tampered := signedOperatorRequest("tampered", "alpha", identity.Workspace, person, device, time.Now())
	tampered.Payload = json.RawMessage(`{"text":"changed after signing"}`)
	if refused := f.send(tampered); refused.Code != protocol.ErrInvalidSeal {
		t.Fatalf("broker accepted changed text: %+v", refused)
	}
	if f.count("messages") != 1 {
		t.Fatal("invalid seal was persisted or delivered")
	}
}

func TestSignedIdentityAcceptanceSealSurvivesBrokerRestart(t *testing.T) {
	database := filepath.Join(t.TempDir(), "signed-restart.db")
	person, device := serverSignedKey("restart-person"), serverSignedKey("restart-device")
	var original protocol.Message
	t.Run("send", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		identity := signedOperatorIdentity(t, f, base64.StdEncoding.EncodeToString(person.Public().(ed25519.PublicKey)))
		agent, _ := signedOperatorPeer(t, f, "alpha")
		original = signedOperatorRequest("signed-restart", "alpha", identity.Workspace, person, device, time.Now())
		requireOperatorAccepted(t, f.send(original), "alpha")
		if got := agent.read(); got.Seal != original.Seal {
			t.Fatal("first delivery lost seal")
		}
	})
	t.Run("replay", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		agent, welcome := signedOperatorPeer(t, f, "alpha")
		got := agent.read()
		if !got.Replayed || got.Seal != original.Seal {
			t.Fatalf("restart changed signed message: %+v", got)
		}
		seal, err := protocol.DecodeSeal(got.Seal)
		operatorNoError(t, err)
		if verdict := signing.Verify(&seal, protocol.RootsOf(welcome.SigningRoots), time.Now()); verdict.State != signing.Verified {
			t.Fatalf("replayed message lost identity: %+v", verdict)
		}
	})
}
