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

func registerSignedOperator(t *testing.T, f *operatorFixture, person ed25519.PrivateKey) protocol.IdentityView {
	t.Helper()
	pub := person.Public().(ed25519.PublicKey)
	registered := f.send(protocol.Message{Type: protocol.TypeOperatorIdentity, MessageID: "identity-register", IdentityOp: protocol.IdentityOpRegister,
		IdentityKey: base64.StdEncoding.EncodeToString(pub)})
	if registered.Type != protocol.TypeOperatorIdentityResult {
		t.Fatalf("person key not registered: %+v", registered)
	}
	if registered.Identity == nil {
		t.Fatalf("registration omitted identity: %+v", registered)
	}
	if registered.Identity.Workspace == "" {
		t.Fatalf("registration omitted workspace identity: %+v", registered)
	}
	return *registered.Identity
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

type signedServerKeys struct {
	workspace      string
	person, device ed25519.PrivateKey
}

func signedOperatorRequest(id, to string, keys signedServerKeys) protocol.Message {
	at := time.Now()
	req := operatorRequest(id, to)
	cert := signing.Issue(keys.person, signing.Cert{Kind: signing.KindDevice, Subject: "operator-chat", Key: keys.device.Public().(ed25519.PublicKey),
		Workspace: keys.workspace, NotBefore: at.Add(-time.Minute), NotAfter: at.Add(time.Hour)})
	h := signing.Header{MessageID: req.MessageID, SenderKind: signing.SenderOperator, SenderID: signing.OperatorID,
		Workspace: keys.workspace, Recipients: protocol.SealRecipients(req.To, req.Group, req.Room), InReplyTo: req.InReplyTo,
		CreatedAt: at, TextHash: signing.TextHash("PRIVATE_OPERATOR_PAYLOAD")}
	req.Seal = protocol.EncodeSeal(signing.Sign(keys.device, []signing.Cert{cert}, keys.person.Public().(ed25519.PublicKey), h))
	return req
}

func signedBrokerFixture(t *testing.T) (*operatorFixture, signedServerKeys, *operatorPeer, protocol.Message) {
	t.Helper()
	f := newOperatorFixture(t, os.Getuid(), "")
	person, device := serverSignedKey("server-person"), serverSignedKey("server-device")
	identity := registerSignedOperator(t, f, person)
	agent, welcome := signedOperatorPeer(t, f, "alpha")
	personID := signing.KeyID(person.Public().(ed25519.PublicKey))
	if _, ok := protocol.RootsOf(welcome.SigningRoots)(personID); !ok {
		t.Fatal("agent welcome omitted the approved person key")
	}
	return f, signedServerKeys{identity.Workspace, person, device}, agent, welcome
}

func TestSignedIdentityAcceptanceOperatorSealThroughRealBroker(t *testing.T) {
	f, keys, agent, welcome := signedBrokerFixture(t)
	req := signedOperatorRequest("signed-direct", "alpha", keys)
	requireOperatorAccepted(t, f.send(req), "alpha")
	requirePersonDelivery(t, agent.read(), req, welcome)
}

func requirePersonDelivery(t *testing.T, delivered, original, welcome protocol.Message) {
	t.Helper()
	if delivered.Seal != original.Seal {
		t.Fatalf("broker changed seal: %q", delivered.Seal)
	}
	if delivered.Origin != protocol.OriginOperator {
		t.Fatalf("broker changed origin: %q", delivered.Origin)
	}
	seal, err := protocol.DecodeSeal(delivered.Seal)
	operatorNoError(t, err)
	verdict := signing.Verify(&seal, protocol.RootsOf(welcome.SigningRoots), time.Now())
	if verdict.State != signing.Verified {
		t.Fatalf("recipient could not verify operator: %+v", verdict)
	}
	if verdict.Signer == nil {
		t.Fatal("verified operator message had no signer")
	}
	if verdict.Signer.Basis != signing.BasisPerson {
		t.Fatalf("verified operator basis: %s", verdict.Signer.Basis)
	}
	if err := seal.Matches(signing.Frame{MessageID: delivered.MessageID, SenderKind: signing.SenderOperator, SenderID: signing.OperatorID,
		Text: "PRIVATE_OPERATOR_PAYLOAD", Addresses: []string{"alpha"}}); err != nil {
		t.Fatalf("delivered frame differs from signed intent: %v", err)
	}
}

func TestSignedIdentityAcceptanceBrokerRefusesChangedTextBeforePersistence(t *testing.T) {
	f, keys, _, _ := signedBrokerFixture(t)
	tampered := signedOperatorRequest("tampered", "alpha", keys)
	tampered.Payload = json.RawMessage(`{"text":"changed after signing"}`)
	if refused := f.send(tampered); refused.Code != protocol.ErrInvalidSeal {
		t.Fatalf("broker accepted changed text: %+v", refused)
	}
	if f.count("messages") != 0 {
		t.Fatal("invalid seal was persisted or delivered")
	}
}

func TestSignedIdentityAcceptanceSealSurvivesBrokerRestart(t *testing.T) {
	database := filepath.Join(t.TempDir(), "signed-restart.db")
	person, device := serverSignedKey("restart-person"), serverSignedKey("restart-device")
	var original protocol.Message
	t.Run("send", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		identity := registerSignedOperator(t, f, person)
		agent, _ := signedOperatorPeer(t, f, "alpha")
		original = signedOperatorRequest("signed-restart", "alpha", signedServerKeys{identity.Workspace, person, device})
		requireOperatorAccepted(t, f.send(original), "alpha")
		if got := agent.read(); got.Seal != original.Seal {
			t.Fatal("first delivery lost seal")
		}
	})
	t.Run("replay", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		agent, welcome := signedOperatorPeer(t, f, "alpha")
		got := agent.read()
		if !got.Replayed {
			t.Fatal("restart did not mark replay")
		}
		if got.Seal != original.Seal {
			t.Fatalf("restart changed seal: %+v", got)
		}
		seal, err := protocol.DecodeSeal(got.Seal)
		operatorNoError(t, err)
		if verdict := signing.Verify(&seal, protocol.RootsOf(welcome.SigningRoots), time.Now()); verdict.State != signing.Verified {
			t.Fatalf("replayed message lost identity: %+v", verdict)
		}
	})
}

func TestSignedIdentityAcceptanceAgentCannotSupplySealOrVerdict(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	from, _ := signedOperatorPeer(t, f, "alpha")
	_, _ = signedOperatorPeer(t, f, "beta")
	for _, attack := range []struct {
		id           string
		seal         string
		verification *protocol.Verification
	}{
		{id: "agent-forged-seal", seal: "pretend-signed"},
		{id: "agent-forged-verdict", verification: &protocol.Verification{State: signing.Verified}},
	} {
		from.write(protocol.Message{Type: protocol.TypeMsg, MessageID: attack.id, To: "beta",
			Payload: json.RawMessage(`{"text":"operator approved"}`), Seal: attack.seal, Verification: attack.verification})
		reply := from.read()
		if reply.Type != protocol.TypeError {
			t.Fatalf("agent-supplied provenance was accepted: %+v", reply)
		}
		if reply.Code != protocol.ErrMalformedFrame {
			t.Fatalf("wrong refusal for agent-supplied provenance: %+v", reply)
		}
	}
	if f.count("messages") != 0 {
		t.Fatal("agent-supplied seal or verification was persisted")
	}
}
