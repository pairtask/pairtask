//go:build linux

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func (side instantSide) shareRemoteAgents(t *testing.T, agents ...string) {
	t.Helper()
	r := side.f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpShare, ContactKey: side.key, ContactAgents: agents, MessageID: "share-agents"})
	operatorEqual(t, "share accepted", r.Type, protocol.TypeOperatorContactResult)
	if r.ContactPage == nil {
		t.Fatalf("share omitted current grants: %+v", r)
	}
	operatorEqual(t, "shared agent count", len(r.ContactPage.Shared), len(agents))
}

func (side instantSide) sendRemoteAgents(t *testing.T, id string, agents ...string) {
	t.Helper()
	r := side.f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend, ContactKey: side.key, ContactTo: agents, ContactText: "remote operator question", MessageID: id})
	operatorEqual(t, "routed send accepted", r.Type, protocol.TypeOperatorContactResult)
}

func (side instantSide) awaitRemoteView(t *testing.T, matches func(protocol.ContactMessageView) bool) protocol.ContactMessageView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r := side.f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpList, ContactKey: side.key})
		if r.ContactPage == nil {
			t.Fatalf("list failed: %+v", r)
		}
		for _, m := range r.ContactPage.Messages {
			if matches(m) {
				return m
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("remote message or receipt did not arrive over live link")
	return protocol.ContactMessageView{}
}

func requireRemoteOperatorDelivery(t *testing.T, got protocol.Message, contact string) {
	t.Helper()
	operatorEqual(t, "remote delivery type", got.Type, protocol.TypeMsg)
	operatorEqual(t, "remote origin", got.Origin, protocol.OriginRemote)
	operatorEqual(t, "remote operator sender", got.From, protocol.OperatorID)
	if got.Remote == nil {
		t.Fatal("remote provenance omitted")
	}
	operatorEqual(t, "canonical contact", got.Remote.Contact, contact)
	operatorEqual(t, "operator sender metadata", got.Remote.Sender, "")
	var payload struct {
		Text string `json:"text"`
	}
	operatorNoError(t, json.Unmarshal(got.Payload, &payload))
	operatorEqual(t, "remote text", payload.Text, "remote operator question")
}

func acknowledgeRemoteAgent(t *testing.T, agent *operatorPeer, id string) {
	t.Helper()
	agent.write(protocol.Message{Type: protocol.TypeAck, MessageID: id, Stage: protocol.StageAdapterReceived})
	agent.write(protocol.Message{Type: protocol.TypeAck, MessageID: id, Stage: protocol.StageAgentAcknowledged})
	r := agent.read()
	operatorEqual(t, "agent acknowledgment persisted", r.Stage, protocol.StageAgentAcknowledged)
}

func remoteReplyRoundTrip(t *testing.T, sides []instantSide) {
	t.Helper()
	sender, receiver := sides[0], sides[1]
	const agentID = "remote:agent/@\nverbatim"
	agent := receiver.f.peer(agentID, 12)
	receiver.shareRemoteAgents(t, agentID)
	sender.sendRemoteAgents(t, "operator-question", agentID)
	got := agent.read()
	requireRemoteOperatorDelivery(t, got, receiver.key)
	acknowledgeRemoteAgent(t, agent, got.MessageID)
	view := sender.awaitRemoteView(t, func(m protocol.ContactMessageView) bool { return m.ID == "operator-question" && len(m.AgentAcks) == 1 })
	operatorEqual(t, "recipient-specific peer receipt", view.AgentAcks[0], agentID)
	if view.ReadAt != nil {
		t.Fatal("agent acknowledgment manufactured human read receipt")
	}
	agent.write(protocol.Message{Type: protocol.TypeMsg, MessageID: "agent-reply", To: got.From, InReplyTo: got.MessageID, Payload: []byte(`{"text":"remote agent answer"}`), Durable: true, AckRequested: true})
	operatorEqual(t, "remote reply accepted", agent.read().Stage, protocol.StageAccepted)
	reply := sender.awaitRemoteView(t, func(m protocol.ContactMessageView) bool {
		return m.ID == "agent-reply" && m.Direction == messaging.DirIn
	})
	operatorEqual(t, "verbatim agent sender", reply.From, agentID)
	operatorEqual(t, "reply text", reply.Text, "remote agent answer")
	operatorEqual(t, "reply points to operator question", reply.ReplyID, "operator-question")
	operatorEqual(t, "reply direction", reply.ReplySide, protocol.ReplyByReceiver)
}

func TestRemoteRoutingE2EOperatorAgentReplyBothDirections(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			p := newInstantPair(t)
			sides := p.sides()
			if reverse {
				sides[0], sides[1] = sides[1], sides[0]
			}
			remoteReplyRoundTrip(t, sides)
			p.assertOneConnection(t)
		})
	}
}

func TestRemoteRoutingE2EUnsharedGroupRefusesWithoutPartialFanout(t *testing.T) {
	p := newInstantPair(t)
	sender, receiver := p.sides()[0], p.sides()[1]
	_ = receiver.f.peer("shared", 12)
	_ = receiver.f.peer("private", 12)
	receiver.shareRemoteAgents(t, "shared")
	sender.sendRemoteAgents(t, "group-refused", "shared", "private")
	row := sender.awaitRemoteView(t, func(m protocol.ContactMessageView) bool {
		return m.ID == "group-refused" && m.State == messaging.StateRefused
	})
	operatorEqual(t, "no existence disclosure", row.RefusalReason, messaging.ReasonNotShared)
	imports, err := receiver.f.st.PendingImports(context.Background(), 100)
	operatorNoError(t, err)
	operatorEqual(t, "no staged partial group", len(imports), 0)
	var rows int
	err = receiver.f.db.QueryRow("SELECT COUNT(*) FROM message_metadata WHERE origin = 'remote'").Scan(&rows)
	operatorNoError(t, err)
	operatorEqual(t, "no imported partial group", rows, 0)
}
