package hub

import (
	"context"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func refsOlderContact(t *testing.T) (*Hub, string) {
	t.Helper()
	h, contact := remoteHubFixture(t)
	bound, err := h.store.PeerMessages().Admit(context.Background(), contact)
	messageWorkerOK(t, err)
	versioned, ok := any(bound).(interface {
		SawVersion(context.Context, byte) error
	})
	if !ok {
		t.Fatal("pairing cannot record negotiated version")
	}
	messageWorkerOK(t, versioned.SawVersion(context.Background(), 11))
	return h, contact
}

func refsRefusalFiles() []map[string]any {
	return []map[string]any{{"path": "opaque.bin", "size": 7, "sha256": strings.Repeat("a", 64)}}
}

func refsUnsupported(t *testing.T, out protocol.Message) {
	t.Helper()
	if out.Type != protocol.TypeError || out.Code != "refs_unsupported" {
		t.Fatalf("older peer refusal: type=%s code=%s detail=%s", out.Type, out.Code, out.Detail)
	}
}

func refsNoQueued(t *testing.T, h *Hub, contact string) {
	t.Helper()
	rows, _, err := h.store.PeerMessages().Page(context.Background(), contact, 0, 10)
	messageWorkerOK(t, err)
	if len(rows) != 0 {
		t.Fatalf("refused references left %d queued rows", len(rows))
	}
}

func TestFileRefsRefusalAcceptanceAgentGetsExplicitOlderPeerReason(t *testing.T) {
	h, contact := refsOlderContact(t)
	agent := remoteHubClient(t, h, "sender", 31)
	remoteHubShare(t, h, contact, agent.ID)
	msg := workspaceMessage(contact, "refs-older-agent")
	msg.To = protocol.OperatorID
	refsHubWithFiles(t, &msg, refsRefusalFiles())
	refsUnsupported(t, workspaceRouteResult(t, h, agent, msg))
	refsNoQueued(t, h, contact)
	plain := workspaceMessage(contact, "plain-after-refs-refusal")
	plain.To = protocol.OperatorID
	if out := workspaceRouteResult(t, h, agent, plain); out.Stage != protocol.StageAccepted {
		t.Fatalf("plain agent send stopped working: type=%s code=%s", out.Type, out.Code)
	}
}

func TestFileRefsRefusalAcceptanceOperatorGetsExplicitOlderPeerReason(t *testing.T) {
	h, contact := refsOlderContact(t)
	msg := protocol.Message{Type: protocol.TypeOperatorContact, ProtocolVersion: 31,
		ContactOp: protocol.ContactOpSend, ContactKey: contact, ContactText: "metadata only", MessageID: "refs-older-operator"}
	refsHubWithFiles(t, &msg, refsRefusalFiles())
	refsUnsupported(t, h.operatorContact(context.Background(), nil, operatorRequest{uid: 0, msg: msg}))
	refsNoQueued(t, h, contact)
}

func TestFileRefsRefusalAcceptancePlainOperatorSendStillWorks(t *testing.T) {
	h, contact := refsOlderContact(t)
	msg := protocol.Message{Type: protocol.TypeOperatorContact, ProtocolVersion: 31,
		ContactOp: protocol.ContactOpSend, ContactKey: contact, ContactText: "plain message", MessageID: "plain-older-operator"}
	out := h.operatorContact(context.Background(), nil, operatorRequest{uid: 0, msg: msg})
	if out.Type != protocol.TypeOperatorContactResult {
		t.Fatalf("plain operator send failed: type=%s code=%s", out.Type, out.Code)
	}
}
