package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

type offlineSeed struct {
	id, pending string
	targets     []string
	operator    bool
	parent      string
}

func (s offlineSeed) recipients() []protocol.Recipient {
	if len(s.targets) == 0 {
		s.targets = []string{protocol.OperatorID}
	}
	var result []protocol.Recipient
	for _, target := range s.targets {
		result = append(result, protocol.Recipient{AgentID: target, SessionID: target + "-session"})
	}
	return result
}

func (s offlineSeed) message(seq uint64) protocol.Message {
	at := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	payload, _ := json.Marshal(map[string]string{"text": "original-body-" + s.id})
	msg := protocol.Message{Type: protocol.TypeMsg, Seq: seq, TS: &at, MessageID: s.id, From: "author", To: protocol.OperatorID,
		Origin: protocol.OriginAgent, Durable: true, AckRequested: true, Payload: payload, InReplyTo: s.parent}
	if len(s.targets) > 0 {
		msg.To = s.targets[0]
	}
	if len(s.targets) > 1 {
		msg.To = "*"
	}
	if s.operator {
		msg.From, msg.Origin = protocol.OperatorID, protocol.OriginOperator
	}
	return msg
}

func (f *offlineInboxFixture) seed(t *testing.T, spec offlineSeed) protocol.Message {
	t.Helper()
	f.seq++
	msg, recipients := spec.message(f.seq), spec.recipients()
	f.enroll(t, recipients)
	f.persist(t, msg, recipients)
	for _, recipient := range recipients {
		f.adopt(t, msg.MessageID, recipient, spec.pending)
	}
	return msg
}

func (f *offlineInboxFixture) enroll(t *testing.T, recipients []protocol.Recipient) {
	t.Helper()
	for _, recipient := range recipients {
		_, err := f.db.ClaimPendingFor(context.Background(), recipient, 25)
		offlineOK(t, err)
	}
}

func (f *offlineInboxFixture) persist(t *testing.T, msg protocol.Message, recipients []protocol.Recipient) {
	t.Helper()
	ctx := context.Background()
	if msg.Origin != protocol.OriginOperator {
		offlineOK(t, f.db.PersistMessage(ctx, msg, "author-session", recipients))
		return
	}
	audit, err := f.db.RecordOperatorAudit(ctx, store.OperatorAudit{Action: "send", PrincipalUID: os.Getuid(), MessageID: msg.MessageID, Result: store.AuditPending})
	offlineOK(t, err)
	scope := store.OperatorScope{Kind: store.ScopeDirect, Members: []string{msg.To}}
	offlineOK(t, f.db.PersistOperatorMessage(ctx, store.OperatorMessage{Msg: msg, Scope: scope, Recipients: recipients, AuditID: audit}))
}

func (f *offlineInboxFixture) adopt(t *testing.T, id string, recipient protocol.Recipient, pending string) {
	t.Helper()
	ack := store.Acknowledgment{MessageID: id, Recipient: recipient, Stage: protocol.StageAdapterReceived, ProtocolVersion: 25}
	_, err := f.db.Acknowledge(context.Background(), ack)
	offlineOK(t, err)
	if recipient.AgentID == pending {
		return
	}
	ack.Stage = protocol.StageAgentAcknowledged
	_, err = f.db.Acknowledge(context.Background(), ack)
	offlineOK(t, err)
}
