//go:build linux

package server

import (
	"fmt"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestWorkspaceE2EAgentInitiationBothDirections(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			p := newInstantPair(t)
			sides := p.sides()
			if reverse {
				sides[0], sides[1] = sides[1], sides[0]
			}
			workspaceAgentRoundTrip(t, sides)
			p.assertOneConnection(t)
		})
	}
}

func workspaceAgentRoundTrip(t *testing.T, sides []instantSide) {
	t.Helper()
	sender, receiver := sides[0], sides[1]
	const first = "first/@\nagent"
	const second = "remote:second/@\nagent"
	a, b := sender.f.peer(first, 13), receiver.f.peer(second, 13)
	sender.shareRemoteAgents(t, first)
	receiver.shareRemoteAgents(t, second)
	a.write(protocol.Message{Type: protocol.TypeWorkspacesGet, RequestID: "workspaces"})
	discovered := a.read()
	operatorEqual(t, "discovery type", discovered.Type, protocol.TypeWorkspacesResult)
	operatorEqual(t, "discovery count", len(discovered.Workspaces), 1)
	operatorEqual(t, "discovery identity", discovered.Workspaces[0].Key, sender.key)
	a.write(protocol.Message{Type: protocol.TypeMsg, Workspace: sender.key, To: second, MessageID: "start", Payload: []byte(`{"text":"agent starts"}`), Durable: true, AckRequested: true})
	operatorEqual(t, "start accepted", a.read().Stage, protocol.StageAccepted)
	got := b.read()
	requireWorkspaceAgent(t, got, receiver.key, first)
	acknowledgeRemoteAgent(t, b, got.MessageID)
	b.write(protocol.Message{Type: protocol.TypeMsg, To: got.From, MessageID: "answer", InReplyTo: got.MessageID, Payload: []byte(`{"text":"agent replies"}`), Durable: true, AckRequested: true})
	operatorEqual(t, "reply accepted", b.read().Stage, protocol.StageAccepted)
	reply := a.read()
	requireWorkspaceAgent(t, reply, sender.key, second)
	operatorEqual(t, "reply resolves original local ID", reply.InReplyTo, "start")
	acknowledgeRemoteAgent(t, a, reply.MessageID)
	a.write(protocol.Message{Type: protocol.TypeMsg, Workspace: sender.key, To: protocol.OperatorID, MessageID: "to-human", Payload: []byte(`{"text":"agent to operator"}`), Durable: true, AckRequested: true})
	operatorEqual(t, "operator send accepted", a.read().Stage, protocol.StageAccepted)
	human := receiver.awaitRemoteView(t, func(m protocol.ContactMessageView) bool { return m.ID == "to-human" && m.Direction == messaging.DirIn })
	operatorEqual(t, "operator sees agent identity", human.From, first)
	operatorEqual(t, "operator sees text", human.Text, "agent to operator")
}

func requireWorkspaceAgent(t *testing.T, m protocol.Message, key, sender string) {
	t.Helper()
	operatorEqual(t, "remote type", m.Type, protocol.TypeMsg)
	operatorEqual(t, "remote origin", m.Origin, protocol.OriginRemote)
	operatorEqual(t, "verbatim sender", m.From, sender)
	if m.Remote == nil {
		t.Fatal("remote provenance omitted")
	}
	operatorEqual(t, "canonical workspace", m.Remote.Contact, key)
	operatorEqual(t, "canonical sender", m.Remote.Sender, sender)
}
