package bridge

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

func remoteOperatorIDFixture(t *testing.T) bridgeSignedFixture {
	t.Helper()
	person, device := bridgeIdentityKey("remote-person"), bridgeIdentityKey("remote-device")
	workspace := signing.KeyID(bridgeIdentityKey("remote-workspace").Public().(ed25519.PublicKey))
	personPublic := person.Public().(ed25519.PublicKey)
	now := time.Now().UTC().Truncate(time.Millisecond)
	text := "\nStatus from work Mac:\n```text\nline one\nline two\n```"
	cert := signing.Issue(person, signing.Cert{Kind: signing.KindDevice, Subject: "operator-chat",
		Key: device.Public().(ed25519.PublicKey), Workspace: workspace,
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)})
	header := signing.Header{MessageID: "work-mac-original-id", SenderKind: signing.SenderOperator,
		SenderID: protocol.OperatorID, Workspace: workspace, Recipients: []string{"alpha"},
		CreatedAt: now, TextHash: signing.TextHash(text)}
	seal := protocol.EncodeSeal(signing.Sign(device, []signing.Cert{cert}, personPublic, header))
	c, remote := pipeClient(t)
	c.protocolVersion, c.agentID = protocol.Version, "alpha"
	c.roots = []protocol.SigningRoot{{KeyID: signing.KeyID(personPublic), Person: true,
		Workspace: workspace, Approved: true}}
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		t.Fatal(err)
	}
	message := protocol.Message{Type: protocol.TypeMsg, MessageID: "remote:local-inbox-id",
		From: protocol.OperatorID, Origin: protocol.OriginRemote, To: "alpha", Payload: payload, Seal: seal,
		Remote: &protocol.Remote{Contact: workspace, MessageID: header.MessageID}}
	return bridgeSignedFixture{client: c, remote: remote, message: message}
}

func TestRemoteSignedIDAcceptanceMultilineOperatorMessageSurvivesLocalIDAndReplay(t *testing.T) {
	for _, replayed := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "replay"}[replayed], func(t *testing.T) {
			f := remoteOperatorIDFixture(t)
			f.message.Replayed = replayed
			verdict := f.deliver(t)
			requireBridgeState(t, verdict, signing.Verified)
			if verdict.Signer == nil || verdict.Signer.Basis != signing.BasisPerson {
				t.Fatalf("paired operator lost person-key provenance: %+v", verdict)
			}
		})
	}
}

func TestRemoteSignedIDAcceptanceChangedOriginalIDIsInvalid(t *testing.T) {
	f := remoteOperatorIDFixture(t)
	f.message.Remote.MessageID = "attacker-changed-id"
	requireBridgeState(t, f.deliver(t), signing.Invalid)
}

func TestRemoteSignedIDAcceptanceLocalOperatorUsesLocalID(t *testing.T) {
	f := newBridgeSignedFixture(t)
	f.message.Remote = &protocol.Remote{MessageID: "spoofed-remote-id"}
	requireBridgeState(t, f.deliver(t), signing.Verified)
}
