package bridge

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

func remoteAgentBridgeFixture(t *testing.T) bridgeSignedFixture {
	t.Helper()
	workspace := bridgeIdentityKey("remote-agent-workspace")
	broker := bridgeIdentityKey("remote-agent-broker")
	workspacePub := workspace.Public().(ed25519.PublicKey)
	workspaceID := signing.KeyID(workspacePub)
	brokerPub := broker.Public().(ed25519.PublicKey)
	now := time.Now().UTC().Truncate(time.Millisecond)
	cert := signing.Issue(workspace, signing.Cert{Kind: signing.KindBroker, Subject: "broker", Key: brokerPub,
		Workspace: workspaceID, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)})
	const self, sender, body = "alpha", "remote-agent", "hello from a paired agent"
	header := signing.Header{MessageID: "remote-agent-message", SenderKind: signing.SenderAgent, SenderID: sender,
		Workspace: workspaceID, Recipients: []string{self, (protocol.PeerLink{Receiver: "receiver-workspace"}).Address()},
		CreatedAt: now, TextHash: signing.TextHash(body)}
	seal := protocol.EncodeSeal(signing.Sign(broker, []signing.Cert{cert}, workspacePub, header))
	c, remote := pipeClient(t)
	c.protocolVersion = protocol.Version
	c.agentID = self
	c.roots = []protocol.SigningRoot{{KeyID: workspaceID, Workspace: workspaceID, Approved: true}}
	message := protocol.Message{Type: protocol.TypeMsg, MessageID: header.MessageID, From: sender,
		Origin: protocol.OriginRemote, To: self, Payload: json.RawMessage(`{"text":"hello from a paired agent"}`), Seal: seal}
	return bridgeSignedFixture{client: c, remote: remote, message: message}
}

func TestContactIdentityAcceptanceBridgeVerifiesPairedAgentIndependently(t *testing.T) {
	f := remoteAgentBridgeFixture(t)
	verdict := f.deliver(t)
	requireBridgeState(t, verdict, signing.Verified)
	if verdict.Signer == nil {
		t.Fatalf("paired agent provenance has no signer: %+v", verdict)
	}
	if verdict.Signer.Basis != signing.BasisWorkspace || verdict.Signer.ID != f.message.From {
		t.Fatalf("paired agent provenance: %+v", verdict)
	}
	for name, mutate := range map[string]func(*bridgeSignedFixture){
		"text":   func(f *bridgeSignedFixture) { f.message.Payload = json.RawMessage(`{"text":"relay changed this"}`) },
		"sender": func(f *bridgeSignedFixture) { f.message.From = "another-agent" },
	} {
		t.Run(name, func(t *testing.T) {
			f := remoteAgentBridgeFixture(t)
			mutate(&f)
			f.message.Verification = &protocol.Verification{State: signing.Verified}
			requireBridgeState(t, f.deliver(t), signing.Invalid)
		})
	}
	f = remoteAgentBridgeFixture(t)
	f.client.roots[0].Approved = false
	requireBridgeState(t, f.deliver(t), signing.Unverified)
}

func TestContactIdentityAcceptanceForeignPersonCannotClaimLocalOperator(t *testing.T) {
	f := newBridgeSignedFixture(t)
	f.client.ownWorkspace = signing.KeyID(bridgeIdentityKey("another-bridge-workspace").Public().(ed25519.PublicKey))
	verdict := f.deliver(t)
	requireBridgeState(t, verdict, signing.Invalid)
	if verdict.Reason != "signed for another workspace" {
		t.Fatalf("foreign operator seal explanation: %q", verdict.Reason)
	}
}
