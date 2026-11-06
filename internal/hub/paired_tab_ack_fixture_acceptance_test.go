package hub

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

type pairedTabAckFixture struct {
	h       *Hub
	key     string
	agent   *Client
	clients map[string]*Client
	raw     *sql.DB
}

func newPairedTabAckFixture(t *testing.T) *pairedTabAckFixture {
	t.Helper()
	h, key := remoteHubFixture(t)
	f := &pairedTabAckFixture{h: h, key: key, clients: map[string]*Client{}}
	f.agent = newClient("local-worker")
	f.agent.SessionID = "local-worker-online"
	f.agent.ProtocolVersion = protocol.Version
	h.connect(f.clients, f.agent)
	messageSyncEqual(t, recv(t, f.agent).Type, protocol.TypeWelcome)
	var err error
	f.raw, err = sql.Open("sqlite", strings.TrimSuffix(h.store.PeerDir(), ".peer"))
	messageWorkerOK(t, err)
	t.Cleanup(func() { f.raw.Close() })
	return f
}
func (f *pairedTabAckFixture) send(t *testing.T, id string) protocol.Message {
	t.Helper()
	msg := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend, MessageID: id, ContactKey: f.key, ContactText: "shared paired-tab body", ContactHere: []string{f.agent.ID}}
	response := f.h.operatorContact(context.Background(), f.clients, operatorRequest{uid: 0, msg: msg})
	messageSyncEqual(t, response.Type, protocol.TypeOperatorContactResult)
	return response
}
func (f *pairedTabAckFixture) delivery(t *testing.T, id string) {
	t.Helper()
	m := recv(t, f.agent)
	messageSyncEqual(t, m.Type, protocol.TypeMsg)
	messageSyncEqual(t, m.MessageID, id)
	messageSyncEqual(t, m.Durable, true)
	messageSyncEqual(t, m.AckRequested, true)
	if m.Conversation == nil {
		t.Fatal("local tagged delivery lost paired conversation")
	}
	messageSyncEqual(t, m.Conversation.Contact, f.key)
}
func (f *pairedTabAckFixture) ack(t *testing.T, id, stage string) bool {
	t.Helper()
	f.h.acknowledge(context.Background(), f.clients, Inbound{From: f.agent, Msg: protocol.Message{Type: protocol.TypeAck, MessageID: id, Stage: stage, AgentID: "caller-forged-agent", SessionID: "caller-forged-session"}})
	select {
	case frame := <-f.agent.Send:
		if frame.Type == protocol.TypeError {
			t.Errorf("online paired-tab %s refused: code=%s detail=%s", stage, frame.Code, frame.Detail)
			return false
		}
		messageSyncEqual(t, frame.Type, protocol.TypeAck)
		messageSyncEqual(t, frame.MessageID, id)
		messageSyncEqual(t, frame.SessionID, f.agent.SessionID)
	default:
		if stage == protocol.StageAgentAcknowledged {
			t.Error("committed agent acknowledgment had no confirmation")
			return false
		}
	}
	return true
}
func (f *pairedTabAckFixture) receipt(t *testing.T, id string) (string, bool, bool) {
	t.Helper()
	var session string
	var received, acked sql.NullString
	messageWorkerOK(t, f.raw.QueryRow(`SELECT session_id, received_at, acknowledged_at FROM durable_inbox WHERE message_id=? AND agent_id=?`, id, f.agent.ID).Scan(&session, &received, &acked))
	return session, received.Valid, acked.Valid
}
func (f *pairedTabAckFixture) reconnect(t *testing.T, session string) {
	t.Helper()
	if f.clients[f.agent.ID] != nil {
		f.h.disconnect(f.clients, f.agent)
	}
	next := newClient(f.agent.ID)
	next.SessionID = session
	next.ProtocolVersion = protocol.Version
	f.agent = next
	f.h.connect(f.clients, next)
	messageSyncEqual(t, recv(t, next).Type, protocol.TypeWelcome)
}
