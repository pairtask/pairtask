package hub

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

func TestSignedAgentRouteAcceptanceBrokerSealsOnceWithWorkspaceBasis(t *testing.T) {
	h, contact := remoteHubFixture(t)
	agent := remoteHubClient(t, h, "sender", 13)
	remoteHubShare(t, h, contact, agent.ID)
	msg := workspaceMessage(contact, "agent-signed-outbound")
	msg.To = protocol.OperatorID
	first := workspaceRouteResult(t, h, agent, msg)
	if first.Stage != protocol.StageAccepted {
		t.Fatalf("agent send was not queued: %+v", first)
	}
	row := queuedAgentSeal(t, h, contact)
	seal := bytes.Clone(row.Seal)
	assertWorkspaceAgentSeal(t, row, agentSealContext{agent.ID, contact, h.identity.Fingerprint()})
	retry := workspaceRouteResult(t, h, agent, msg)
	if retry.Stage != protocol.StageAccepted || !retry.Repeated {
		t.Fatalf("agent retry was not accepted as a retry: %+v", retry)
	}
	if retry.Seq != first.Seq {
		t.Fatalf("agent retry changed send identity: %+v", retry)
	}
	row = queuedAgentSeal(t, h, contact)
	if !bytes.Equal(row.Seal, seal) {
		t.Fatalf("agent retry replaced seal: %+v", row)
	}
}

func queuedAgentSeal(t *testing.T, h *Hub, contact string) messaging.Row {
	t.Helper()
	rows, err := h.store.PeerMessages().Offer(context.Background(), contact, time.Now(), messaging.SessionLimit)
	messageWorkerOK(t, err)
	if len(rows) != 1 || len(rows[0].Seal) == 0 {
		t.Fatalf("broker did not seal agent's queued message: %+v", rows)
	}
	return rows[0]
}

type agentSealContext struct{ agentID, contact, workspace string }

func assertWorkspaceAgentSeal(t *testing.T, row messaging.Row, want agentSealContext) {
	t.Helper()
	parsed, err := signing.ParseSeal(row.Seal)
	messageWorkerOK(t, err)
	roots := protocol.RootsOf([]protocol.SigningRoot{{KeyID: want.workspace, Workspace: want.workspace, Approved: true}})
	verified := signing.Verify(&parsed, roots, time.Now())
	if verified.State != signing.Verified || verified.Signer == nil {
		t.Fatalf("agent provenance is not verified: %+v", verified)
	}
	if verified.Signer.Basis != signing.BasisWorkspace || verified.Signer.ID != want.agentID {
		t.Fatalf("agent provenance is not workspace-attested: %+v", verified)
	}
	frame := signing.Frame{MessageID: row.ID, SenderKind: signing.SenderAgent, SenderID: want.agentID,
		Text: row.Text, Addresses: []string{(protocol.PeerLink{Receiver: want.contact}).Address()}}
	if err := parsed.Matches(frame); err != nil {
		t.Fatalf("seal does not bind queued agent message: %v", err)
	}
	if got := parsed.Header.Recipients; !slices.Equal(got, []string{protocol.OperatorID, (protocol.PeerLink{Receiver: want.contact}).Address()}) {
		t.Fatalf("agent seal did not bind exact recipient set: %v", got)
	}
}

func TestSignedAgentRouteAcceptanceWelcomeRootsExcludeUnconfirmedContact(t *testing.T) {
	h, confirmed := remoteHubFixture(t)
	ctx := context.Background()
	const pending = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	now := time.Now()
	_, err := h.store.Commit(ctx, peer.PairingOutcome{OperationID: "pending-root-op", InvitationID: "pending-root-inv",
		IdentityKey: pending, Name: "Pending", TranscriptID: make([]byte, 64), At: now})
	messageWorkerOK(t, err)
	_, err = h.store.Accept(ctx, pending)
	messageWorkerOK(t, err)
	roots, err := h.signingRoots(ctx)
	messageWorkerOK(t, err)
	for _, root := range roots {
		if root.KeyID == pending {
			t.Fatalf("unconfirmed contact gained a trusted signing root: %+v", root)
		}
	}
	if !hasSigningRoot(roots, confirmed) {
		t.Fatalf("confirmed contact missing from roots: %+v", roots)
	}
	_, err = h.store.Deliver(ctx, pending, peer.PairingAccepted, now)
	messageWorkerOK(t, err)
	roots, err = h.signingRoots(ctx)
	messageWorkerOK(t, err)
	if !hasSigningRoot(roots, pending) {
		t.Fatalf("confirmed contact did not gain a root: %+v", roots)
	}
}

func TestSignedAgentRouteAcceptanceReplySealNamesParentDirection(t *testing.T) {
	h, contact := remoteHubFixture(t)
	agent := remoteHubClient(t, h, "replying-agent", protocol.SealVersion)
	parent := remoteHubImported(t, h, contact, agent)
	reply := remoteHubReply(parent, "signed-agent-reply")
	h.route(context.Background(), map[string]*Client{agent.ID: agent}, Inbound{From: agent, Msg: reply})
	ack := recv(t, agent)
	if ack.Stage != protocol.StageAccepted {
		t.Fatalf("agent reply was not queued: %+v", ack)
	}
	rows, err := h.store.PeerMessages().Offer(context.Background(), contact, time.Now(), messaging.SessionLimit)
	messageWorkerOK(t, err)
	if len(rows) != 1 {
		t.Fatalf("reply was not queued once: %+v", rows)
	}
	if len(rows[0].Seal) == 0 || rows[0].ReplyTo.Author != messaging.ReplyByReceiver {
		t.Fatalf("reply was not sealed with directional parent: %+v", rows)
	}
	seal, err := signing.ParseSeal(rows[0].Seal)
	messageWorkerOK(t, err)
	if seal.Header.InReplyTo != "receiver:"+rows[0].ReplyTo.ID {
		t.Fatalf("reply seal lost parent direction: %q", seal.Header.InReplyTo)
	}
}

func hasSigningRoot(roots []protocol.SigningRoot, key string) bool {
	return slices.Contains(roots, protocol.SigningRoot{KeyID: key, Workspace: key, Approved: true})
}
