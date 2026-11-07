//go:build linux

package main

import (
	"encoding/json"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestInboxArchiveAcceptanceAgentAndOperatorReplaysAreDuplicateOnWire(t *testing.T) {
	f := newOfflineInbox(t)
	agent := f.seed(t, offlineSeed{id: "agent-archived"})
	operator := f.seed(t, offlineSeed{id: "operator-archived", operator: true, targets: []string{"reader"}})
	f.archive(t)
	marker := json.RawMessage(f.payload(t, operator.MessageID))
	w := f.wire(t)
	p := offlineDial(t, w.agent)
	welcome := p.exchange(t, protocol.Message{Type: "hello", AgentID: "author", ProtocolVersion: 25})
	assertStatusEqual(t, "agent connected", welcome.Type, "welcome")
	response := p.exchange(t, protocol.Message{Type: "send", MessageID: agent.MessageID, To: agent.To, Payload: agent.Payload, Durable: true, AckRequested: true})
	offlineDuplicate(t, response)
	for _, payload := range []json.RawMessage{operator.Payload, marker} {
		response = offlineDial(t, w.operator).exchange(t, protocol.Message{Type: protocol.TypeOperatorSend, MessageID: operator.MessageID, To: operator.To, Payload: payload})
		offlineDuplicate(t, response)
	}
	assertStatusEqual(t, "no replay routed a new row", offlineCount(t, f.database, `SELECT COUNT(*) FROM messages`), int64(2))
}

func offlineDuplicate(t *testing.T, response protocol.Message) {
	t.Helper()
	assertStatusEqual(t, "duplicate frame", response.Type, "error")
	assertStatusEqual(t, "archived identity reserved", response.Code, "duplicate_message")
	assertStatusEqual(t, "never a repeated acceptance", response.Repeated, false)
}
